import { useCallback, useEffect, useRef, useState } from 'react'
import { request, streamURL } from '../../lib/api'

/*
 * Chargement et suivi du journal d'une task.
 *
 * Le journal est une suite d'octets numérotée de façon stable : la même position
 * désigne le même endroit qu'il soit encore en tranches ou déjà scellé. Tout ce
 * qui suit en découle — la reprise du flux, le chargement du début à la demande,
 * et l'ancrage sur une ligne.
 */

export type LogLine = {
  timestamp: string
  text: string
}

type Segment = {
  content: string
  offset: number
  next_offset: number
  size: number
  sealed: boolean
}

/** Quantité chargée quand l'utilisateur remonte dans l'historique. */
const EARLIER_CHUNK = 128 * 1024

type State = {
  content: string
  /** Position du premier octet chargé. Zéro veut dire « le début est là ». */
  firstOffset: number
  size: number
  loading: boolean
  error: string | null
}

const INITIAL: State = { content: '', firstOffset: 0, size: 0, loading: true, error: null }

export function useTaskLog(runName: string, taskName: string, sealed: boolean) {
  const [state, setState] = useState<State>(INITIAL)

  // Position de reprise du flux. Une référence et non un état : elle est lue par
  // le gestionnaire d'événements, qui n'est pas recréé à chaque rendu, et la
  // faire entrer dans les dépendances rouvrirait le flux à chaque ligne reçue.
  const nextOffset = useRef<number | null>(null)
  const [ready, setReady] = useState(false)

  const path = `/runs/${encodeURIComponent(runName)}/tasks/${encodeURIComponent(taskName)}`

  // Chargement initial, puis rechargement au scellement.
  //
  // Le second n'est pas superflu : le flux se ferme dès que le journal est scellé,
  // et les dernières lignes peuvent avoir été écrites entre le dernier événement
  // reçu et la fermeture. L'objet scellé fait foi — le relire garantit que ce qui
  // s'affiche est bien le journal complet, et non ce que le flux a eu le temps de
  // livrer.
  useEffect(() => {
    let cancelled = false

    request<Segment>(`${path}/log`)
      .then((segment) => {
        if (cancelled) return
        nextOffset.current = segment.next_offset
        setState({
          content: segment.content,
          firstOffset: segment.offset,
          size: segment.size,
          loading: false,
          error: null,
        })
        setReady(true)
      })
      .catch(() => {
        if (cancelled) return
        setState((previous) => ({ ...previous, loading: false, error: 'Le journal n’a pas pu être chargé.' }))
      })

    return () => {
      cancelled = true
    }
  }, [path, sealed])

  // Suivi : uniquement tant que le journal peut grossir, et seulement une fois la
  // position initiale connue — sinon le flux renverrait la fin une seconde fois.
  useEffect(() => {
    if (sealed || !ready) return

    // La position de départ passe par la requête : EventSource ne permet pas de
    // poser `Last-Event-ID` à la première connexion, le navigateur ne l'envoie
    // que lors de ses propres reconnexions.
    const stream = new EventSource(streamURL(`${path}/log/stream?offset=${nextOffset.current ?? 0}`))

    stream.addEventListener('log', (event) => {
      const message = event as MessageEvent<string>
      const id = Number(message.lastEventId)
      if (Number.isFinite(id)) nextOffset.current = id

      setState((previous) => ({
        ...previous,
        // Le serveur envoie des lignes complètes ; le séparateur est rétabli
        // ici parce que le protocole SSE l'a consommé.
        content: previous.content + message.data + '\n',
        size: Number.isFinite(id) ? Math.max(previous.size, id) : previous.size,
      }))
    })

    // Le journal ne changera plus : fermer évite de sonder indéfiniment.
    stream.addEventListener('sealed', () => stream.close())

    // Aucun traitement d'erreur : EventSource reconnecte de lui-même en
    // envoyant `Last-Event-ID`. Fermer sur erreur supprimerait cette reprise.
    return () => stream.close()
  }, [path, sealed, ready])

  /** Charge le début manquant. */
  const loadEarlier = useCallback(async () => {
    const from = Math.max(0, state.firstOffset - EARLIER_CHUNK)
    if (state.firstOffset <= 0) return

    try {
      const segment = await request<Segment>(`${path}/log?offset=${from}&length=${state.firstOffset - from}`)
      setState((previous) => ({
        ...previous,
        content: segment.content + previous.content,
        firstOffset: segment.offset,
      }))
    } catch {
      setState((previous) => ({ ...previous, error: 'Le début du journal n’a pas pu être chargé.' }))
    }
  }, [path, state.firstOffset])

  return {
    lines: splitLines(state.content),
    size: state.size,
    loading: state.loading,
    error: state.error,
    /** complete dit que le début du journal est chargé : plus rien à remonter. */
    complete: state.firstOffset === 0,
    loadEarlier,
  }
}

function splitLines(content: string): LogLine[] {
  if (!content) return []

  return content
    .split('\n')
    .filter((line) => line.length > 0)
    .map((line) => {
      // kubelet préfixe chaque ligne d'un RFC3339Nano suivi d'un espace. Le
      // préfixe est retiré de l'affichage — il est du bruit à l'écran — mais
      // conservé pour l'infobulle : c'est lui qui permet de recouper avec un
      // autre journal.
      const separator = line.indexOf(' ')
      if (separator === -1) return { timestamp: '', text: line }

      const candidate = line.slice(0, separator)
      if (!/^\d{4}-\d{2}-\d{2}T/.test(candidate)) return { timestamp: '', text: line }

      return { timestamp: candidate, text: line.slice(separator + 1) }
    })
}
