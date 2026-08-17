package auth

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DBFlowStore conserve les transactions d'authentification en base.
//
// C'est [FlowStore] rendu réplicable. Le stockage en mémoire supposait que
// `/auth/login` et `/auth/callback` atteignent la même instance ; dès deux
// répliques sans affinité de session, une connexion sur deux échouait — le
// `state` était introuvable sur la réplique qui recevait le retour.
//
// Le coût est une écriture et une suppression par tentative de connexion. Il est
// réel mais négligeable au regard de la fréquence : on se connecte une fois par
// session de travail, pas une fois par requête.
type DBFlowStore struct {
	pool *pgxpool.Pool
	ttl  time.Duration
	now  func() time.Time
}

// NewDBFlowStore construit le magasin persistant.
func NewDBFlowStore(pool *pgxpool.Pool, ttl time.Duration) *DBFlowStore {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &DBFlowStore{pool: pool, ttl: ttl, now: time.Now}
}

// Begin crée une transaction d'authentification et rend son état.
func (s *DBFlowStore) Begin(ctx context.Context, redirectAfter string) (FlowState, error) {
	state, err := randomToken(32)
	if err != nil {
		return FlowState{}, err
	}
	nonce, err := randomToken(32)
	if err != nil {
		return FlowState{}, err
	}
	verifier, err := randomToken(64)
	if err != nil {
		return FlowState{}, err
	}

	now := s.now().UTC()
	flow := FlowState{
		State:         state,
		Nonce:         nonce,
		CodeVerifier:  verifier,
		RedirectAfter: redirectAfter,
		CreatedAt:     now,
	}

	_, err = s.pool.Exec(ctx,
		`insert into auth_flows (state, nonce, code_verifier, redirect_after, created_at, expires_at)
		 values ($1, $2, $3, nullif($4, ''), $5, $6)`,
		state, nonce, verifier, redirectAfter, now, now.Add(s.ttl))
	if err != nil {
		return FlowState{}, fmt.Errorf("ouverture de la transaction d'authentification: %w", err)
	}
	return flow, nil
}

// Consume rend l'état associé et le retire.
//
// `delete ... returning` en une seule requête, et c'est ce qui donne l'usage
// unique : deux retours concurrents portant le même `state` — un rejeu — ne
// peuvent pas réussir tous les deux, PostgreSQL n'en laissant aboutir qu'un.
// Un `select` suivi d'un `delete` ouvrirait exactement cette fenêtre.
//
// Le retrait a lieu que l'état soit valide ou expiré : un `state` déjà présenté
// ne doit pas pouvoir l'être une seconde fois, même échu.
func (s *DBFlowStore) Consume(ctx context.Context, state string) (FlowState, error) {
	var flow FlowState
	var redirectAfter *string
	var expiresAt time.Time

	err := s.pool.QueryRow(ctx,
		`delete from auth_flows where state = $1
		 returning state, nonce, code_verifier, redirect_after, created_at, expires_at`, state).
		Scan(&flow.State, &flow.Nonce, &flow.CodeVerifier, &redirectAfter,
			&flow.CreatedAt, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// Rejeu, expiration déjà purgée, ou `state` forgé : les trois se
			// traitent pareil, et distinguer les cas renseignerait un attaquant.
			return FlowState{}, ErrUnknownState
		}
		return FlowState{}, fmt.Errorf("lecture de la transaction d'authentification: %w", err)
	}

	if redirectAfter != nil {
		flow.RedirectAfter = *redirectAfter
	}

	if s.now().After(expiresAt) {
		return FlowState{}, ErrUnknownState
	}
	return flow, nil
}

// PurgeExpiredFlows supprime les transactions abandonnées.
//
// Appelée périodiquement par le `worker`, comme la purge des sessions : une
// connexion commencée puis interrompue laisse une ligne que personne ne
// consommera jamais.
func (s *DBFlowStore) PurgeExpiredFlows(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `delete from auth_flows where expires_at <= $1`, s.now().UTC())
	if err != nil {
		return 0, fmt.Errorf("purge des transactions d'authentification: %w", err)
	}
	return tag.RowsAffected(), nil
}
