// Package web sert l'interface, embarquée dans le binaire.
//
// Embarquer plutôt que monter un volume ou déployer un second conteneur : le
// binaire et l'interface qu'il sert sont alors le même artefact, versionné
// ensemble. Une interface déployée en avance sur son API appelle des routes qui
// n'existent pas encore, et le symptôme — des 404 sur une page qui s'affiche —
// ne désigne pas sa cause.
package web

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
)

// all: inclut les fichiers commençant par un point, dont le .gitkeep qui permet
// à ce répertoire d'exister dans le dépôt sans y verser de produit de build.
//
//go:embed all:dist
var assets embed.FS

// ErrNotBuilt signale une interface absente du binaire.
var ErrNotBuilt = errors.New("interface non construite")

// Handler sert les fichiers statiques et renvoie index.html pour toute route
// inconnue.
//
// Ce repli est ce qui fait fonctionner les URL profondes : `/releases/1.8.0` est
// une route du navigateur, pas un fichier. Sans lui, un rechargement de page ou
// un lien partagé donnerait un 404 alors que la navigation interne marche —
// panne classique, et déroutante parce qu'elle ne se produit jamais en cliquant.
func Handler(logger *slog.Logger) (http.Handler, error) {
	dist, err := fs.Sub(assets, "dist")
	if err != nil {
		return nil, fmt.Errorf("racine des fichiers statiques: %w", err)
	}

	index, err := fs.ReadFile(dist, "index.html")
	if err != nil {
		// Cas normal en développement backend : le front n'a pas été construit.
		// Refuser de démarrer serait excessif — l'API, elle, fonctionne.
		logger.Warn("interface non embarquée: lancer `make web`", "error", err)
		return notBuiltHandler(), nil
	}

	policy := contentSecurityPolicy(index)
	logger.Info("interface embarquée", "csp_script_src", policy.scriptSrc)

	files := http.FileServerFS(dist)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		policy.apply(w)

		clean := strings.TrimPrefix(r.URL.Path, "/")
		if clean == "" {
			serveIndex(w, index)
			return
		}

		if _, err := fs.Stat(dist, clean); err != nil {
			// Route du navigateur, pas un fichier : index.html, et surtout pas
			// un 404 — c'est au routeur côté client de décider.
			serveIndex(w, index)
			return
		}

		// Les fichiers d'`assets/` portent une empreinte dans leur nom : leur
		// contenu ne change jamais sous un nom donné, ils sont donc cachables
		// indéfiniment. index.html, lui, ne doit jamais l'être, sans quoi un
		// déploiement resterait invisible.
		if strings.HasPrefix(clean, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		files.ServeHTTP(w, r)
	}), nil
}

func serveIndex(w http.ResponseWriter, index []byte) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(index)
}

func notBuiltHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = fmt.Fprintln(w, "Interface non construite. Lancer `make web`, puis reconstruire le binaire.")
	})
}

// inlineScript capture le contenu des scripts en ligne d'index.html.
var inlineScript = regexp.MustCompile(`(?s)<script(?:\s[^>]*)?>(.*?)</script>`)

type csp struct {
	scriptSrc string
	value     string
}

func (p csp) apply(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", p.value)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "same-origin")
}

// contentSecurityPolicy construit une politique sans `unsafe-inline`.
//
// index.html porte un script en ligne — celui qui applique le thème avant le
// premier rendu, sans quoi un aplat blanc apparaît une frame à chaque
// chargement en thème sombre. Un script en ligne exige normalement
// `unsafe-inline`, qui vide la politique de sa substance : elle autoriserait
// alors aussi le script qu'une injection y placerait.
//
// L'alternative est d'autoriser ce script précis par son empreinte. Elle est
// calculée ici à partir du fichier réellement embarqué : le jour où le script
// change, la politique suit, sans intervention et sans risque d'oubli.
func contentSecurityPolicy(index []byte) csp {
	sources := []string{"'self'"}

	for _, match := range inlineScript.FindAllSubmatch(index, -1) {
		// Un <script src=…> a un corps vide : il est couvert par 'self'.
		if len(strings.TrimSpace(string(match[1]))) == 0 {
			continue
		}
		sum := sha256.Sum256(match[1])
		sources = append(sources, "'sha256-"+base64.StdEncoding.EncodeToString(sum[:])+"'")
	}

	scriptSrc := strings.Join(sources, " ")

	return csp{
		scriptSrc: scriptSrc,
		value: strings.Join([]string{
			"default-src 'self'",
			"script-src " + scriptSrc,
			// Tailwind produit des styles en ligne pour les propriétés
			// personnalisées ; les interdire casserait la mise en page.
			"style-src 'self' 'unsafe-inline'",
			"img-src 'self' data:",
			"font-src 'self'",
			// Les polices sont auto-hébergées et l'API est de même origine :
			// aucune destination externe n'est légitime.
			"connect-src 'self'",
			"frame-ancestors 'none'",
			"base-uri 'none'",
			"form-action 'self'",
		}, "; "),
	}
}
