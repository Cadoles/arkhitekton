package http

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

/*
Flux d'événements côté serveur.

Le suivi en direct n'est pas alimenté par un canal en mémoire, contrairement à ce
que décrit la doc 06 §6.5. La raison est structurelle : le collecteur tourne dans
le `worker`, le flux SSE est servi par l'`api`, et ce sont deux processus. Un canal
en mémoire ne les relie pas.

Trois voies étaient possibles :

  - faire transiter chaque ligne par PostgreSQL NOTIFY — volume élevé pour un
    canal dont la charge utile est bornée à 8 000 octets ;
  - servir le flux depuis le worker — il n'a ni serveur HTTP ni service, et
    l'exposer ferait de l'instance unique un point de passage du trafic
    utilisateur ;
  - **suivre le stockage**, retenu ici.

L'api relit périodiquement ce que le collecteur a écrit et pousse la différence.
La latence est donc bornée par l'intervalle de flush du collecteur, pas par le
sondage. Le procédé a trois avantages qui compensent largement ce délai : il
fonctionne avec plusieurs répliques d'`api`, il est repris à l'octet près par
`Last-Event-ID`, et c'est exactement le même chemin de lecture que l'historique —
il n'y a pas deux implémentations à garder d'accord.
*/

// sseHeartbeat maintient la connexion ouverte à travers les intermédiaires.
//
// nginx ferme une connexion inactive, et l'Ingress du dashboard porte un
// `proxy-read-timeout` d'une heure : un pipeline silencieux pendant ce délai
// verrait son flux coupé sans raison visible. Un commentaire SSE ne déclenche
// aucun événement côté client.
const sseHeartbeat = 20 * time.Second

// sseWriter encapsule le protocole.
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
}

// newSSEWriter prépare la réponse. Rend false si le client ne peut pas être servi
// en flux — cas d'un intermédiaire qui met en tampon.
func newSSEWriter(w http.ResponseWriter) (*sseWriter, bool) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache")
	header.Set("Connection", "keep-alive")
	// Sans cet en-tête, nginx accumule les événements et les délivre en bloc :
	// l'interface affiche un run figé, puis saute à son état final.
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	return &sseWriter{w: w, flusher: flusher}, true
}

// event émet un événement nommé, avec son identifiant de reprise.
func (s *sseWriter) event(name, id, data string) error {
	var builder strings.Builder
	if id != "" {
		fmt.Fprintf(&builder, "id: %s\n", id)
	}
	if name != "" {
		fmt.Fprintf(&builder, "event: %s\n", name)
	}
	// Une charge utile multiligne doit être découpée : un `\n` brut terminerait
	// l'événement, et le client ne recevrait que sa première ligne.
	for line := range strings.SplitSeq(data, "\n") {
		fmt.Fprintf(&builder, "data: %s\n", line)
	}
	builder.WriteString("\n")

	if _, err := s.w.Write([]byte(builder.String())); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// comment émet un battement de cœur, invisible pour le client.
func (s *sseWriter) comment(text string) error {
	if _, err := fmt.Fprintf(s.w, ": %s\n\n", text); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}
