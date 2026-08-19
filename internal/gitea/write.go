package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

/*
 * Écriture sur la forge : pose de tags, et rien d'autre.
 *
 * Client **distinct** de celui en lecture, comme annoncé quand celui-ci a été
 * écrit. La séparation n'est pas cosmétique : le transport de lecture refuse tout
 * ce qui n'est pas GET ou HEAD, et l'assouplir aurait fait qu'un appel de lecture
 * mal écrit puisse écrire. Ici la permission est explicite, et son périmètre
 * tient en une méthode.
 *
 * `DELETE` reste refusé : poser un tag est réparable — on en pose un autre —
 * tandis qu'en supprimer un détruit la référence d'un livrable, geste qu'aucun
 * automate ne devrait pouvoir faire (B3).
 */

// ErrTagExists signale un tag déjà posé.
//
// Distinct d'une erreur d'écriture : reposer un tag après un échec partiel est un
// cas normal, et l'appelant doit pouvoir continuer plutôt que de s'arrêter.
var ErrTagExists = errors.New("gitea: le tag existe déjà")

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
		return nil, fmt.Errorf("%w (%s %s): ce client pose des tags, il n'en supprime pas",
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
