import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { request } from '../../lib/api'

/*
 * Les agents IA : ce qu'un agent demande à faire au nom d'une personne, et les
 * agents qu'elle a autorisés, à révoquer.
 */

export const SCOPE_READ = 'dashboard:read'
export const SCOPE_RUN = 'dashboard:run'

/** Une demande d'autorisation en attente de consentement. */
export type AgentRequest = {
  id: string
  /** Le nom que l'agent s'est donné : rien ne l'atteste. */
  client: string
  /** L'hôte où le navigateur sera renvoyé, qui dit d'où vient l'agent. */
  redirect_host: string
  scopes: string[]
}

export type Agent = {
  id: number
  name: string
  /** Le propriétaire, donné à un administrateur seulement. */
  owner?: string
  scopes: string[]
  created_at: string
  last_used?: string
}

export type Agents = {
  /** L'adresse du serveur MCP, à donner à l'agent. */
  mcp_url: string
  /** Vrai quand la liste est celle de tout le monde. */
  all: boolean
  agents: Agent[]
}

export function useAgentRequest(id: string) {
  return useQuery({
    queryKey: ['agent-request', id],
    queryFn: () => request<AgentRequest>(`/oauth/requests/${encodeURIComponent(id)}`),
    retry: false,
  })
}

/** Accorde ou refuse la demande ; le serveur rend l'adresse où renvoyer le navigateur. */
export function useAnswerAgentRequest(id: string) {
  return useMutation({
    mutationFn: (answer: { approve: true; scopes: string[] } | { approve: false }) =>
      request<{ redirect: string }>(
        `/oauth/requests/${encodeURIComponent(id)}/${answer.approve ? 'approve' : 'deny'}`,
        { method: 'POST', body: JSON.stringify(answer.approve ? { scopes: answer.scopes } : {}) },
      ),
  })
}

const AGENTS = ['agents']

export function useAgents() {
  return useQuery({ queryKey: AGENTS, queryFn: () => request<Agents>('/agents') })
}

export function useRevokeAgent() {
  const queryClient = useQueryClient()
  return useMutation({
    mutationFn: (id: number) => request<void>(`/agents/${id}`, { method: 'DELETE' }),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: AGENTS }),
  })
}

/** Renvoie le navigateur vers l'agent, hors du dashboard. */
export const browser = {
  leaveTo: (url: string) => window.location.assign(url),
}
