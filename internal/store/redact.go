package store

import (
	"errors"
	"net/url"
	"strings"
)

// redactError retire le mot de passe du DSN d'un message d'erreur.
//
// pgx cite volontiers la chaîne de connexion dans ses erreurs d'analyse. Ces
// erreurs finissent dans les logs, parfois dans un ticket, parfois dans une
// capture d'écran. Le mot de passe n'a rien à y faire, et c'est précisément au
// moment d'un incident que l'on copie-colle sans relire.
//
// Le message est conservé par ailleurs : masquer l'erreur entière rendrait le
// diagnostic impossible au moment où on en a le plus besoin.
func redactError(err error, dsn string) error {
	if err == nil {
		return nil
	}

	password := passwordFromDSN(dsn)
	if password == "" {
		return err
	}

	message := err.Error()
	if !strings.Contains(message, password) {
		return err
	}

	return errors.New(strings.ReplaceAll(message, password, "xxxxx"))
}

// passwordFromDSN extrait le mot de passe d'une URL de connexion. Rend une chaîne
// vide pour les DSN au format clé-valeur (`host=... password=...`), que cette
// fonction ne prétend pas couvrir : pgxpool.ParseConfig les accepte, mais le
// déploiement du dashboard utilise la forme URL.
func passwordFromDSN(dsn string) string {
	parsed, err := url.Parse(strings.TrimSpace(dsn))
	if err != nil || parsed.User == nil {
		return ""
	}
	password, hasPassword := parsed.User.Password()
	if !hasPassword {
		return ""
	}
	return password
}
