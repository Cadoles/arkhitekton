import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'

export default defineConfig({
  plugins: [react(), tailwindcss()],

  // La SPA est embarquée dans le binaire Go (`internal/web/dist`), et servie par
  // le rôle `api`. Construire ailleurs obligerait à une étape de copie, donc à
  // un moyen de l'oublier.
  // `emptyOutDir` efface aussi les fichiers cachés, dont le `.gitkeep` qui fait
  // exister ce répertoire dans le dépôt — sans lui, `//go:embed all:dist` ne
  // compile plus sur un clone neuf. Le script `postbuild` le repose : le faire
  // dans package.json plutôt que dans le Makefile le rend valable partout où le
  // build tourne, y compris dans l'étage Docker qui appelle npm directement.
  build: {
    outDir: '../../internal/web/dist',
    emptyOutDir: true,
  },

  /*
   * Les tests d'interface.
   *
   * Ils existent parce que quatre défauts consécutifs ont été trouvés à la main,
   * dans des chemins que rien n'exerçait : un formulaire qui soumettait son état
   * initial plutôt que ce qu'il affichait, un bouton visible là où il ne devait
   * pas l'être. Aucun n'était détectable autrement qu'en cliquant.
   *
   * `jsdom` plutôt qu'un vrai navigateur : ces composants ne dépendent ni de la
   * mise en page ni du rendu, seulement de ce qu'ils envoient et de ce qu'ils
   * montrent. Un navigateur coûterait des minutes pour la même réponse.
   */
  test: {
    environment: 'jsdom',
    globals: true,
    setupFiles: ['./src/test/setup.ts'],
    // Les tests vivent auprès de ce qu'ils éprouvent : un fichier déplacé
    // emmène ses tests, et leur absence se voit dans le même répertoire.
    include: ['src/**/*.test.ts', 'src/**/*.test.tsx'],
  },

  server: {
    // En développement, Vite sert l'interface et relaie l'API au binaire Go.
    // Sans cela, le cookie de session — `SameSite=Lax`, posé sur l'origine du
    // backend — ne serait pas renvoyé depuis l'origine de Vite.
    proxy: {
      '/api': {
        target: process.env.VITE_API_TARGET ?? 'http://127.0.0.1:8080',
        changeOrigin: false,
      },
    },
  },
})
