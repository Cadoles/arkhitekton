// Package webhook porte les appels sortants configurables, et le contrôle de
// leur cible.
//
// # Le risque, nommé
//
// Une URL saisie dans l'interface devient une requête HTTP émise **depuis
// l'intérieur du cluster**, par le worker, qui porte un jeton de compte de
// service. C'est une falsification de requête côté serveur offerte, et les cibles
// intéressantes sont connues :
//
//   - `169.254.169.254` — les métadonnées cloud, donc des identifiants d'instance ;
//   - l'API Kubernetes elle-même ;
//   - les services internes du cluster, que rien n'expose autrement ;
//   - la boucle locale du pod.
//
// # La parade suit le précédent de la maison
//
// `internal/redmine` et `internal/gitea` refusent l'écriture **dans leur
// transport**, avant le réseau, plutôt que par la discipline des appelants —
// parce qu'une garantie structurelle ne s'oublie pas. Le même geste ici : le
// contrôle est dans le `Control` du composeur, appelé par la pile réseau juste
// avant de connecter, avec l'adresse **réellement résolue**.
//
// C'est ce qui défait le réamorçage DNS. Vérifier l'URL puis laisser le client
// résoudre à nouveau laisse une fenêtre où le nom change de réponse ; ici, il n'y
// a pas de seconde résolution à tromper — ce qui est contrôlé est ce qui est
// connecté.
//
// # Ce qui est refusé, et ce qui ne l'est pas
//
// Refusé : les plages privées, la boucle locale, le lien-local, le multicast, et
// toute redirection. Autorisé sous réserve d'une déclaration explicite du
// déploiement : les cibles privées — un dashboard local a besoin de `localhost` —
// et le clair.
//
// Ce paquet ne décide pas **quoi** envoyer. Il décide seulement qu'on n'ira pas
// là où il ne faut pas.
package webhook

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
)

// ErrBlockedTarget signale une cible que le transport refuse d'atteindre.
//
// Nommée pour que l'appelant puisse la distinguer d'une panne réseau : « refusé »
// se corrige dans la configuration de la réaction, « injoignable » chez le
// destinataire, et confondre les deux envoie chercher au mauvais endroit.
var ErrBlockedTarget = errors.New("cible refusée")

// cgnat est la plage de report d'adresses des opérateurs (RFC 6598).
//
// Absente des prédicats de la bibliothèque standard, et pourtant du même ordre
// que les plages privées : elle désigne le réseau de l'hébergeur, pas Internet.
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// checkAddress refuse une adresse que le transport ne doit pas atteindre.
//
// Prend l'adresse **résolue**, telle que la pile réseau s'apprête à la composer.
// C'est ce qui distingue ce contrôle d'une inspection d'URL : un nom qui répond
// `93.184.216.34` à la vérification et `127.0.0.1` à la connexion ne passe pas
// deux fois ici, il passe une fois — au moment qui compte.
func checkAddress(address string, allowPrivate bool) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: adresse %q illisible", ErrBlockedTarget, address)
	}

	parsed, err := netip.ParseAddr(host)
	if err != nil {
		// La pile réseau ne compose que des adresses. Un nom ici serait le signe
		// que le contrôle n'est pas au bon endroit, et le laisser passer
		// annulerait tout le mécanisme.
		return fmt.Errorf("%w: %q n'est pas une adresse résolue", ErrBlockedTarget, host)
	}

	// Une adresse IPv4 encapsulée dans de l'IPv6 doit être jugée sur son fond :
	// `::ffff:169.254.169.254` désigne bien les métadonnées cloud, et les
	// prédicats IPv6 ne le verraient pas.
	if parsed.Is4In6() {
		parsed = parsed.Unmap()
	}

	if reason := forbidden(parsed); reason != "" {
		if allowPrivate && privatelyReachable(parsed) {
			return nil
		}
		return fmt.Errorf("%w: %s (%s)", ErrBlockedTarget, parsed, reason)
	}
	return nil
}

// forbidden nomme ce qui interdit une adresse, ou rend une chaîne vide.
//
// Le motif est rendu plutôt qu'un booléen : « refusé » sans raison envoie
// chercher une panne, alors que « boucle locale » se corrige en une lecture.
func forbidden(address netip.Addr) string {
	switch {
	case !address.IsValid():
		return "adresse invalide"
	case address.IsUnspecified():
		return "adresse non spécifiée"
	case address.IsLoopback():
		return "boucle locale"
	case address.IsLinkLocalUnicast():
		// La plus dangereuse : `169.254.169.254` sert les métadonnées d'instance
		// chez tous les hébergeurs, identifiants compris.
		return "lien-local — métadonnées d'instance"
	case address.IsLinkLocalMulticast(), address.IsInterfaceLocalMulticast(), address.IsMulticast():
		return "multicast"
	case address.IsPrivate():
		return "plage privée"
	case cgnat.Contains(address):
		return "plage de report d'opérateur"
	default:
		return ""
	}
}

// privatelyReachable dit qu'une adresse relève de l'autorisation explicite du
// déploiement.
//
// Elle couvre la boucle locale et les plages privées — de quoi faire tourner un
// dashboard local ou joindre un service interne délibérément désigné. Elle ne
// couvre **jamais** le lien-local : les métadonnées d'instance ne sont pas une
// cible qu'un déploiement veut atteindre, et l'autorisation générale ne doit pas
// les rouvrir par la bande.
func privatelyReachable(address netip.Addr) bool {
	return address.IsLoopback() || address.IsPrivate() || cgnat.Contains(address)
}
