package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"forge.cadoles.com/vcarroy/mse-dashboard/internal/provider"
	"net/url"
	"strings"
	"time"
)

/*
 * Écriture sur la forge : ce qui s'ajoute, jamais ce qui s'enlève.
 *
 * Client **distinct** de celui en lecture, comme annoncé quand celui-ci a été
 * écrit. La séparation n'est pas cosmétique : le transport de lecture refuse tout
 * ce qui n'est pas GET ou HEAD, et l'assouplir aurait fait qu'un appel de lecture
 * mal écrit puisse écrire. Ici la permission est explicite, et son périmètre tient
 * dans une liste de méthodes HTTP.
 *
 * Trois gestes aujourd'hui : poser un tag, poser un statut de commit, ajouter un
 * commentaire. Les deux derniers viennent des actions que le plugin Gitea déclare.
 * Ils partagent une propriété qui vaut d'être nommée : **ils ajoutent**. Un statut
 * remplace le précédent de même contexte, un commentaire s'ajoute à un fil ; ni
 * l'un ni l'autre ne fait disparaître ce qui était là.
 *
 * `DELETE` reste refusé, et `PATCH` avec lui : poser un tag est réparable — on en
 * pose un autre — tandis qu'en supprimer un détruit la référence d'un livrable,
 * geste qu'aucun automate ne devrait pouvoir faire (B3). La même prudence vaut
 * pour un commentaire d'autrui qu'une action mal réglée pourrait réécrire.
 */

// ErrTagExists signale un tag déjà posé. Enveloppe l'issue du contrat : reposer
// un tag après un échec partiel est un cas normal, et l'appelant doit pouvoir le
// reconnaître sans savoir à quelle forge il parle.
var ErrTagExists = fmt.Errorf("gitea: %w", provider.ErrTagExists)

var writeMethods = map[string]bool{
	http.MethodGet:  true,
	http.MethodHead: true,
	http.MethodPost: true,
}

type writeTransport struct {
	next  http.RoundTripper
	token string
}

func (t *writeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if !writeMethods[request.Method] {
		return nil, fmt.Errorf("%w (%s %s): ce client ajoute, il ne retire ni ne réécrit",
			ErrWriteForbidden, request.Method, request.URL.Path)
	}

	authenticated := request.Clone(request.Context())
	authenticated.Header.Set("Authorization", "token "+t.token)
	authenticated.Header.Set("Accept", "application/json")
	return t.next.RoundTrip(authenticated)
}

// Writer pose des tags sur le dépôt de l'application.
type Writer struct {
	baseURL *url.URL
	owner   string
	repo    string
	http    *http.Client
}

// NewWriter construit un client capable de poser des tags.
func NewWriter(options Options) (*Writer, error) {
	read, err := New(options)
	if err != nil {
		return nil, err
	}

	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}

	return &Writer{
		baseURL: read.baseURL,
		owner:   read.owner,
		repo:    read.repo,
		http: &http.Client{
			Timeout:   timeout,
			Transport: &writeTransport{next: http.DefaultTransport, token: options.Token},
		},
	}, nil
}

// CreateTag pose un tag léger sur un commit.
//
// Le commit est passé explicitement, jamais une branche : poser un tag sur une
// branche le placerait sur ce qu'elle désigne à cet instant, et deux releases
// créées à une minute d'intervalle pourraient viser des commits différents en
// croyant viser le même point.
func (w *Writer) CreateTag(ctx context.Context, tag, commit, message string) error {
	if strings.TrimSpace(tag) == "" || strings.TrimSpace(commit) == "" {
		return fmt.Errorf("gitea: tag et commit sont requis pour poser un tag")
	}

	payload, err := json.Marshal(map[string]string{
		"tag_name": tag,
		"target":   commit,
		"message":  message,
	})
	if err != nil {
		return fmt.Errorf("gitea: encodage de la demande de tag: %w", err)
	}

	target := w.baseURL.JoinPath("/api/v1/repos/" + w.owner + "/" + w.repo + "/tags")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("gitea: requête de tag: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := w.http.Do(request)
	if err != nil {
		return fmt.Errorf("gitea: pose du tag %q: %w", tag, err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusCreated:
		return nil
	case response.StatusCode == http.StatusConflict,
		response.StatusCode == http.StatusUnprocessableEntity:
		// Gitea rend 409 ou 422 selon la version quand le tag existe. Les deux
		// disent la même chose, et ce n'est pas un échec de la release.
		return fmt.Errorf("%w: %s", ErrTagExists, tag)
	case response.StatusCode == http.StatusForbidden,
		response.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("gitea: pose du tag refusée (%d) — le jeton a-t-il la portée write:repository ?",
			response.StatusCode)
	default:
		return fmt.Errorf("gitea: pose du tag %q: la forge a répondu %d", tag, response.StatusCode)
	}
}

// SetCommitStatus pose un statut sur un commit.
//
// Le `context` est la clé de remplacement : la forge n'affiche qu'un statut par
// contexte et par commit, et reposer le même nom écrase le précédent. C'est ce
// qui permet à une chaîne de publier `pending` au départ puis de le conclure,
// plutôt que d'empiler des états contradictoires sur la même demande de fusion.
//
// Le commit est passé résolu. Une branche serait acceptée par la forge et
// poserait le statut sur ce qu'elle désigne à cet instant — donc, une minute plus
// tard, ailleurs.
func (w *Writer) SetCommitStatus(ctx context.Context, commit, state, context_, description, targetURL string) error {
	commit = strings.TrimSpace(commit)
	if commit == "" || strings.TrimSpace(state) == "" || strings.TrimSpace(context_) == "" {
		return fmt.Errorf("gitea: commit, état et contexte sont requis pour poser un statut")
	}

	return w.add(ctx, "/api/v1/repos/"+w.owner+"/"+w.repo+"/statuses/"+commit,
		map[string]string{
			"state":       state,
			"context":     context_,
			"description": description,
			"target_url":  targetURL,
		},
		fmt.Sprintf("pose du statut %q sur %s", context_, shortCommit(commit)))
}

// CommentOnPullRequest ajoute un commentaire à une demande de fusion.
//
// Passe par l'API des *issues* et non par celle des demandes de fusion, et ce
// n'est pas un contournement : dans Gitea comme dans Forgejo, une demande de
// fusion **est** une issue augmentée, et son fil de discussion vit sous cette
// route. Celle des demandes de fusion ne porte que les revues.
func (w *Writer) CommentOnPullRequest(ctx context.Context, number int, body string) error {
	if number <= 0 {
		return fmt.Errorf("gitea: numéro de demande de fusion invalide: %d", number)
	}
	if strings.TrimSpace(body) == "" {
		// Refusé plutôt qu'envoyé : un commentaire vide se poste sans erreur et
		// laisse une trace muette dans le fil d'une équipe, qu'il faut aller
		// retirer à la main.
		return fmt.Errorf("gitea: un commentaire vide n'est pas posté")
	}

	return w.add(ctx,
		fmt.Sprintf("/api/v1/repos/%s/%s/issues/%d/comments", w.owner, w.repo, number),
		map[string]string{"body": body},
		fmt.Sprintf("commentaire sur la demande de fusion n° %d", number))
}

// add émet un POST JSON et juge la réponse.
//
// Le `geste` décrit ce qu'on tentait, en français, et sert à composer l'erreur :
// « la forge a répondu 403 » sans dire de quoi oblige à retrouver l'appel dans le
// journal pour comprendre ce qui a été refusé.
func (w *Writer) add(ctx context.Context, path string, payload map[string]string, geste string) error {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("gitea: encodage de la demande (%s): %w", geste, err)
	}

	target := w.baseURL.JoinPath(path)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("gitea: requête (%s): %w", geste, err)
	}
	request.Header.Set("Content-Type", "application/json")

	response, err := w.http.Do(request)
	if err != nil {
		return fmt.Errorf("gitea: %s: %w", geste, err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return nil
	case response.StatusCode == http.StatusForbidden,
		response.StatusCode == http.StatusUnauthorized:
		return fmt.Errorf("gitea: %s refusé (%d) — le jeton a-t-il la portée write:repository ?",
			geste, response.StatusCode)
	case response.StatusCode == http.StatusNotFound:
		// Distingué du refus : sur ces routes, la forge rend 404 quand le jeton
		// n'a pas la visibilité du dépôt aussi bien que quand la cible n'existe
		// pas. Le dire évite de chercher un commit qui est bien là.
		return fmt.Errorf("gitea: %s: introuvable (404) — la cible ou le dépôt "+
			"est hors de portée du jeton", geste)
	default:
		return fmt.Errorf("gitea: %s: la forge a répondu %d", geste, response.StatusCode)
	}
}

// shortCommit abrège un SHA pour un message.
func shortCommit(commit string) string {
	if len(commit) <= 8 {
		return commit
	}
	return commit[:8]
}

// ResolveCommit rend le SHA vers lequel pointe une référence.
//
// Une release doit désigner un commit et non une branche : une branche bouge, et
// reconstruire la même release six mois plus tard produirait alors une autre
// image. C'est le premier maillon de l'invariant de version (B17).
func (c *Client) ResolveCommit(ctx context.Context, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", fmt.Errorf("gitea: référence vide")
	}

	query := url.Values{}
	query.Set("sha", ref)
	query.Set("limit", "1")
	// Le corps des commits ne sert à rien ici et pèse : `stat=false` évite de
	// rapatrier le détail des fichiers modifiés pour n'en lire que le SHA.
	query.Set("stat", "false")
	query.Set("verification", "false")
	query.Set("files", "false")

	body, err := c.get(ctx, "/repos/"+c.owner+"/"+c.repo+"/commits", query)
	if err != nil {
		return "", err
	}

	var commits []struct {
		SHA string `json:"sha"`
	}
	if err := json.Unmarshal(body, &commits); err != nil {
		return "", fmt.Errorf("gitea: réponse de résolution illisible: %w", err)
	}
	if len(commits) == 0 || commits[0].SHA == "" {
		return "", fmt.Errorf("gitea: la référence %q ne désigne aucun commit", ref)
	}
	return commits[0].SHA, nil
}
