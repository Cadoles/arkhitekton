/*
 * Formats d'affichage. Locale `fr-FR` figée, et non celle du navigateur :
 * l'interface est en français, et une date affichée au format américain à côté
 * d'un libellé français se lit mal — surtout `03/04` en `04/03`, où l'erreur
 * passe inaperçue.
 */

const DATE_TIME = new Intl.DateTimeFormat('fr-FR', { dateStyle: 'short', timeStyle: 'medium' })
const DATE = new Intl.DateTimeFormat('fr-FR', { dateStyle: 'medium' })

/** Une taille en octets, à l'unité qui la rend lisible. */
export function formatBytes(size: number): string {
  if (size < 1024) return `${size} o`
  if (size < 1024 * 1024) return `${Math.round(size / 1024)} Kio`
  return `${(size / (1024 * 1024)).toFixed(1)} Mio`
}

/** Rend l'entrée telle quelle si elle n'est pas une date : mieux vaut une valeur
 * brute lisible qu'un « Invalid Date ». */
export function formatDateTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : DATE_TIME.format(date)
}

export function formatDate(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : DATE.format(date)
}

/**
 * Âge relatif, affiché quand la fraîcheur d'un bloc est en cause.
 *
 * Le dashboard croise des sources qui peuvent être périmées ou muettes — Redmine
 * en tête. Un chiffre sans son âge est un chiffre qu'on croit à jour
 * (doc 08 §8.2).
 */
export function formatAge(value: string): string {
  const date = new Date(value)
  if (Number.isNaN(date.getTime())) return value

  const seconds = Math.floor((Date.now() - date.getTime()) / 1000)
  if (seconds < 60) return "à l'instant"
  if (seconds < 3600) return `il y a ${Math.floor(seconds / 60)} min`
  if (seconds < 86400) return `il y a ${Math.floor(seconds / 3600)} h`
  return `il y a ${Math.floor(seconds / 86400)} j`
}
