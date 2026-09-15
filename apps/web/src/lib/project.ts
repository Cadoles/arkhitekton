/**
 * Le projet dont les tags font foi, et la composition des URL qui en dépendent.
 *
 * Une release et une version n'existent que **dans** un projet : le tag `1.8.0`
 * n'est unique qu'en son sein. Les routes de l'API le portent donc dans leur
 * chemin — `/projects/mse/releases/1.8.0` — et toute URL composée ici passe par
 * `projectPath`.
 *
 * La liste vient de `/me`, seul endroit qu'une personne connectée peut lire quel
 * que soit son rôle : l'écran d'administration des projets exige `admin`, et
 * l'interface a besoin de composer ses URL dès le premier écran.
 *
 * `/me` rendait auparavant **un** projet — celui qui se construit. Déclarer un
 * second dépôt constructible faisait échouer cette résolution, et l'interface se
 * retrouvait sans aucune URL composable. Le choix appartient donc à qui regarde,
 * et il est retenu d'une visite à l'autre.
 *
 * Un module et une variable de module, plutôt qu'un contexte React : ces URL sont
 * composées dans des `queryFn` et des fonctions de mutation, hors de tout arbre de
 * composants. Un contexte obligerait à passer le slug à chaque appel, ce qui
 * reviendrait à le répéter partout — exactement ce qu'on cherche à éviter.
 */

/** Clé de mémorisation du choix, d'une visite à l'autre. */
const STORAGE_KEY = 'mse-dashboard.project'

let current = ''
let available: ProjectChoice[] = []

/** Un projet tel que `/me` le nomme, avec les modules allumés pour lui. */
export type ProjectChoice = { slug: string; label: string; modules?: string[] }

/**
 * Retient les projets lisibles et arrête le projet courant.
 *
 * Le choix mémorisé l'emporte quand il existe encore ; sinon le premier de la
 * liste. Un slug retenu qui a disparu — projet supprimé, renommé — ne doit pas
 * laisser l'interface sur une URL morte.
 */
export function setProjects(projects: ProjectChoice[]): void {
  available = projects

  const remembered = readRemembered()
  const keep = projects.find((project) => project.slug === remembered)
  current = keep?.slug ?? projects[0]?.slug ?? ''
}

/** Rend les projets entre lesquels choisir. */
export function projects(): ProjectChoice[] {
  return available
}

/** Change de projet courant et mémorise le choix. */
export function selectProject(slug: string): void {
  if (!available.some((project) => project.slug === slug)) return
  current = slug
  try {
    window.localStorage.setItem(STORAGE_KEY, slug)
  } catch {
    // Stockage refusé — navigation privée, quota. Le choix vaut pour la session
    // en cours, ce qui est mieux que de faire échouer le changement de projet.
  }
}

function readRemembered(): string {
  try {
    return window.localStorage.getItem(STORAGE_KEY) ?? ''
  } catch {
    return ''
  }
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
