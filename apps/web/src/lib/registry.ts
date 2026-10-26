import type { AppModule } from './modules'

/*
 * Les modules qu'un plugin installé enregistre en se chargeant.
 *
 * Le script d'un plugin appelle `registerModule` (exporté par
 * `@arkhitekton/host`) avec sa définition ; le noyau les ajoute à ceux que son
 * build connaît. Séparé de `runtime.ts` : `@arkhitekton/host` le réexporte, et
 * `runtime.ts` importe `@arkhitekton/host`.
 */

const registered: AppModule[] = []

/** Ajoute le module d'un plugin installé. Un second enregistrement du même
 *  identifiant remplace le premier : un script chargé deux fois ne double pas
 *  le menu. */
export function registerModule(module: AppModule): void {
  const index = registered.findIndex((entry) => entry.id === module.id)
  if (index >= 0) registered.splice(index, 1, module)
  else registered.push(module)
}

/** Les modules enregistrés, dans l'ordre de leur chargement. */
export function registeredModules(): AppModule[] {
  return [...registered]
}
