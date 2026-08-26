/**
 * Le projet dont les tags font foi, et la composition des URL qui en dépendent.
 *
 * Une release et une version n'existent que **dans** un projet : le tag `1.8.0`
 * n'est unique qu'en son sein. Les routes de l'API le portent donc dans leur
 * chemin — `/projects/mse/releases/1.8.0` — et toute URL composée ici passe par
 * `projectPath`.
 *
 * Le slug vient de `/me`, seul endroit qu'une personne connectée peut lire quel
 * que soit son rôle : la liste des projets exige `admin`, et l'interface a besoin
 * de composer ses URL dès le premier écran.
 *
 * Un module et une variable de module, plutôt qu'un contexte React : ces URL sont
 * composées dans des `queryFn` et des fonctions de mutation, hors de tout arbre de
 * composants. Un contexte obligerait à passer le slug à chaque appel, ce qui
 * reviendrait à le répéter partout — exactement ce qu'on cherche à éviter.
 */

let current = ''

/**
 * Retient le projet courant. Appelé une fois, à la réception de `/me`.
 */
export function setCurrentProject(slug: string): void {
  current = slug
}

/** Rend le projet courant, vide tant que `/me` n'a pas répondu. */
export function currentProject(): string {
  return current
}

/**
 * Compose le chemin d'une ressource rattachée au projet courant.
 *
 * Lève plutôt que de composer une URL creuse : `/projects//releases` ne
 * correspond à aucune route, et l'API répondrait 404 sur une adresse qui ne dit
 * pas ce qui manque. Une exception ici nomme la cause — le projet n'est pas encore
 * connu — et remonte à l'écran par le mécanisme d'erreur des requêtes.
 */
export function projectPath(path: string): string {
  if (!current) {
    throw new Error("projet courant inconnu : l'identité n'a pas encore été chargée")
  }
  const suffix = path.startsWith('/') ? path : `/${path}`
  return `/projects/${encodeURIComponent(current)}${suffix}`
}
