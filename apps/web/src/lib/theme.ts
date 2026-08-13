import { useCallback, useEffect, useState } from 'react'

/*
 * Thème clair / sombre. Même clé de stockage que la maquette, pour que le script
 * anti-flash de index.html et ce module lisent la même chose.
 */

const STORAGE_KEY = 'dashboard-theme'

export type Theme = 'light' | 'dark'

function systemTheme(): Theme {
  return window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'
}

function storedTheme(): Theme | null {
  try {
    const value = localStorage.getItem(STORAGE_KEY)
    return value === 'light' || value === 'dark' ? value : null
  } catch {
    // localStorage peut être refusé (navigation privée stricte, politique
    // d'entreprise). Le thème suit alors le système, ce qui reste utilisable.
    return null
  }
}

export function useTheme() {
  const [theme, setTheme] = useState<Theme>(() => storedTheme() ?? systemTheme())

  useEffect(() => {
    document.documentElement.classList.toggle('dark', theme === 'dark')
    try {
      localStorage.setItem(STORAGE_KEY, theme)
    } catch {
      // Sans persistance, le choix vaut pour la session. Échouer serait pire.
    }
  }, [theme])

  useEffect(() => {
    // Le thème système ne reprend la main que si l'utilisateur n'a rien choisi :
    // un choix explicite ne doit pas être écrasé au coucher du soleil.
    const media = window.matchMedia('(prefers-color-scheme: dark)')
    const onChange = (event: MediaQueryListEvent) => {
      if (storedTheme() === null) setTheme(event.matches ? 'dark' : 'light')
    }
    media.addEventListener('change', onChange)
    return () => media.removeEventListener('change', onChange)
  }, [])

  const toggle = useCallback(() => {
    setTheme((current) => (current === 'dark' ? 'light' : 'dark'))
  }, [])

  return { theme, isDark: theme === 'dark', toggle }
}
