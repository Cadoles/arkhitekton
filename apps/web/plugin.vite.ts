import { fileURLToPath } from 'node:url'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { defineConfig } from 'vite'
import { SHARED } from './src/lib/shared'

/*
 * Le build de l'interface d'un plugin installé.
 *
 * Lancé depuis un arbre composé (les sources du noyau, le module du plugin dans
 * `src/plugins/<id>/`) :
 *
 *   ARKHITEKTON_PLUGIN_ENTRY=src/plugins/<id>/entry.ts \
 *   ARKHITEKTON_PLUGIN_OUT=<répertoire de l'interface> \
 *   npx vite build --config plugin.vite.ts
 *
 * Il produit `index.js`, un script qui appelle `registerModule`, et
 * `index.css`. L'entrée importe `@arkhitekton/host/plugin.css`, la feuille des
 * utilitaires du plugin (`src/styles/plugin.css`), sous ce nom stable. Ni React, ni le routeur, ni le cache de requêtes, ni l'hôte n'y
 * sont : le script les prend dans `window.__arkhitekton`, sous les noms de
 * `src/lib/shared.ts`.
 */

const entry = process.env.ARKHITEKTON_PLUGIN_ENTRY
const outDir = process.env.ARKHITEKTON_PLUGIN_OUT
if (!entry || !outDir) {
  throw new Error('ARKHITEKTON_PLUGIN_ENTRY et ARKHITEKTON_PLUGIN_OUT sont requis')
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: {
      '@arkhitekton/host/plugin.css': fileURLToPath(new URL('./src/styles/plugin.css', import.meta.url)),
    },
  },
  define: { 'process.env.NODE_ENV': JSON.stringify('production') },
  build: {
    outDir,
    emptyOutDir: true,
    lib: {
      entry,
      formats: ['iife'],
      name: 'arkhitektonPlugin',
      fileName: () => 'index.js',
      cssFileName: 'index',
    },
    rollupOptions: {
      external: Object.keys(SHARED),
      output: {
        globals: Object.fromEntries(
          Object.entries(SHARED).map(([id, name]) => [id, `__arkhitekton.${name}`]),
        ),
      },
    },
  },
})
