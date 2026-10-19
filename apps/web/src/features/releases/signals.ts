import type { ReleaseJob } from './api'

/*
 * Ce qui reste ici après le passage au score par geste.
 *
 * Trois tables vivaient dans ce fichier — signal → pipeline, pipeline → signaux,
 * signal → famille de rapports. Elles disaient ce que le serveur sait désormais :
 * un composant du score porte le geste qui l'a produit, donc sa famille de
 * pipeline et sa famille de rapports (doc 15 §15.6).
 *
 * Elles devenaient fausses dès qu'un projet déclarait deux campagnes de même
 * nature : elles ne relancent pas le même pipeline, et une table indexée par
 * signal ne pouvait pas le dire.
 *
 * Ne subsiste que la question « une construction est-elle en cours ? », qui ne
 * porte sur aucun signal. Les libellés et les rôles des pipelines sont rendus par
 * le serveur depuis le catalogue.
 */

/** Le job en file ou en cours qui remesure ce composant, s'il y en a un.
 *
 *  Sur la **famille de pipeline** du geste, rendue par le serveur, et non sur une
 *  table signal → pipeline écrite ici. Celle-ci existait en trois exemplaires et
 *  devenait fausse dès qu'un projet déclarait deux campagnes de même nature :
 *  elles ne relancent pas le même pipeline (doc 15 §15.6). */
export function runningJobFor(ref: string | undefined, jobs: ReleaseJob[]): ReleaseJob | undefined {
  if (!ref) return undefined
  return jobs.find(
    (job) => job.kind === ref && (job.state === 'queued' || job.state === 'running'),
  )
}

/**
 * Vrai si une construction est en cours.
 *
 * Distincte d'une vérification : pendant une construction, **aucun** signal n'est
 * mesurable — les tests qui suivront porteront sur une autre image. Le score
 * affiché est donc celui de l'image précédente, et le dire vaut mieux que de
 * laisser croire qu'il décrit ce qui se construit.
 */
export function buildIsRunning(jobs: ReleaseJob[]): boolean {
  return jobs.some(
    (job) => job.role === 'build' && (job.state === 'queued' || job.state === 'running'),
  )
}
