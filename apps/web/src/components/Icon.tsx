/*
 * Jeu d'icônes de la maquette, repris trait pour trait.
 *
 * Une map nom → tracés plutôt qu'une bibliothèque : vingt icônes pèsent moins
 * qu'une dépendance, et surtout elles restent identiques à celles que le client a
 * validées. Toutes sont dessinées sur la même grille 24×24, trait de 2, extrémités
 * arrondies — c'est ce qui les fait cohabiter sans retouche.
 */

const PATHS = {
  check: '<path d="M20 6 9 17l-5-5"/>',
  cross: '<path d="M18 6 6 18M6 6l12 12"/>',
  alert:
    '<path d="M10.29 3.86 1.82 18a2 2 0 0 0 1.71 3h16.94a2 2 0 0 0 1.71-3L13.71 3.86a2 2 0 0 0-3.42 0z"/><path d="M12 9v4"/><path d="M12 17h.01"/>',
  skip: '<circle cx="12" cy="12" r="9"/><path d="m4.9 4.9 14.2 14.2"/>',
  minus: '<path d="M5 12h14"/>',
  'arrow-up': '<path d="M12 19V5"/><path d="m5 12 7-7 7 7"/>',
  'arrow-down': '<path d="M12 5v14"/><path d="m19 12-7 7-7-7"/>',
  'arrow-right': '<path d="M5 12h14"/><path d="m12 5 7 7-7 7"/>',
  'arrow-left': '<path d="M19 12H5"/><path d="m12 19-7-7 7-7"/>',
  external:
    '<path d="M18 13v6a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V8a2 2 0 0 1 2-2h6"/><path d="M15 3h6v6"/><path d="M10 14 21 3"/>',
  chevron: '<path d="m6 9 6 6 6-6"/>',
  clock: '<circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/>',
  commit: '<circle cx="12" cy="12" r="3"/><path d="M3 12h6"/><path d="M15 12h6"/>',
  branch:
    '<circle cx="6" cy="6" r="2.5"/><circle cx="6" cy="18" r="2.5"/><circle cx="18" cy="8" r="2.5"/><path d="M6 8.5v7"/><path d="M18 10.5a6 6 0 0 1-6 6H8.5"/>',
  shield: '<path d="M12 22s8-4 8-10V5l-8-3-8 3v7c0 6 8 10 8 10z"/>',
  gauge: '<path d="M12 14 16 10"/><path d="M3.34 19a10 10 0 1 1 17.32 0"/>',
  beaker: '<path d="M9 3h6"/><path d="M10 3v6.5L5 18a2 2 0 0 0 2 3h10a2 2 0 0 0 2-3l-5-8.5V3"/><path d="M7.5 14h9"/>',
  doc: '<path d="M14 3H6a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2V9z"/><path d="M14 3v6h6"/>',
  sun: '<circle cx="12" cy="12" r="4"/><path d="M12 2v2"/><path d="M12 20v2"/><path d="m4.93 4.93 1.41 1.41"/><path d="m17.66 17.66 1.41 1.41"/><path d="M2 12h2"/><path d="M20 12h2"/><path d="m6.34 17.66-1.41 1.41"/><path d="m19.07 4.93-1.41 1.41"/>',
  moon: '<path d="M12 3a6.4 6.4 0 0 0 9 9 9 9 0 1 1-9-9Z"/>',
  layers:
    '<path d="m12 2 9 5-9 5-9-5 9-5z"/><path d="m3 12 9 5 9-5"/><path d="m3 17 9 5 9-5"/>',
  queue: '<path d="M4 6h16"/><path d="M4 12h16"/><path d="M4 18h10"/>',
  cog: '<circle cx="12" cy="12" r="3"/><path d="M12 2v3"/><path d="M12 19v3"/><path d="m4.9 4.9 2.1 2.1"/><path d="m17 17 2.1 2.1"/><path d="M2 12h3"/><path d="M19 12h3"/><path d="m4.9 19.1 2.1-2.1"/><path d="m17 7 2.1-2.1"/>',
  tag: '<path d="M20.6 13.4 12 22l-9-9V3h10l7.6 7.6a2 2 0 0 1 0 2.8z"/><path d="M7.5 7.5h.01"/>',
  user: '<circle cx="12" cy="8" r="4"/><path d="M4 21a8 8 0 0 1 16 0"/>',
  download: '<path d="M12 3v12"/><path d="m7 11 5 5 5-5"/><path d="M4 21h16"/>',
  link: '<path d="M9.5 14.5a4 4 0 0 0 5.66 0l3-3a4 4 0 0 0-5.66-5.66l-1 1"/><path d="M14.5 9.5a4 4 0 0 0-5.66 0l-3 3a4 4 0 0 0 5.66 5.66l1-1"/>',
  expand: '<path d="M9 3H3v6"/><path d="M15 21h6v-6"/><path d="M3 3l7 7"/><path d="M21 21l-7-7"/>',
  collapse: '<path d="M3 9h6V3"/><path d="M21 15h-6v6"/><path d="M3 3l6 6"/><path d="M21 21l-6-6"/>',
} as const

export type IconName = keyof typeof PATHS

/** Le nom d'une icône venue des données, ou `fallback` s'il n'en désigne
 *  aucune : une ressource d'interface nomme ses icônes en texte. */
export function iconOr(name: string | undefined, fallback: IconName = 'doc'): IconName {
  return name !== undefined && name in PATHS ? (name as IconName) : fallback
}

type IconProps = {
  name: IconName
  className?: string
}

export function Icon({ name, className }: IconProps) {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={2}
      strokeLinecap="round"
      strokeLinejoin="round"
      // Décorative par défaut : le sens est toujours porté par le texte à côté.
      // Une icône annoncée aux lecteurs d'écran doublerait ce texte.
      aria-hidden="true"
      focusable="false"
      className={className}
      // Les tracés sont des constantes du module, jamais des données d'API : il
      // n'y a pas d'entrée utilisateur à ce niveau.
      dangerouslySetInnerHTML={{ __html: PATHS[name] }}
    />
  )
}
