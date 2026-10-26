/*
 * Ce que le noyau prête au script d'un plugin installé, et sous quel nom.
 *
 * La clé est l'import que le plugin écrit, la valeur le nom sous lequel il le
 * trouve dans `window.__arkhitekton`. Lu par le noyau (`runtime.ts`) et par le
 * preset de build des plugins (`plugin.vite.ts`) : un seul endroit pour les deux.
 */
export const SHARED = {
  react: 'react',
  'react/jsx-runtime': 'jsx',
  'react-dom': 'reactDom',
  'react-router': 'router',
  '@tanstack/react-query': 'query',
  '@arkhitekton/host': 'host',
} as const
