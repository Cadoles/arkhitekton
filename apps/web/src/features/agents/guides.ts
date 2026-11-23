/*
 * Brancher un agent : la marche à suivre pour les agents les plus courants.
 *
 * Tous parlent MCP en HTTP « streamable » et découvrent seuls l'autorisation
 * OAuth du dashboard : on leur donne l'adresse, ils ouvrent le navigateur sur
 * l'écran d'autorisation. Seule la façon de déclarer le serveur change.
 */

export type GuideStep = {
  text: string
  /** Une commande ou un fichier à copier. */
  code?: string
}

export type Guide = {
  id: string
  label: string
  steps: GuideStep[]
  /** Une précision propre à cet agent. */
  note?: string
}

const NAME = 'arkhitekton'

function json(value: unknown): string {
  return JSON.stringify(value, null, 2)
}

export function guides(url: string): Guide[] {
  return [
    {
      id: 'claude-code',
      label: 'Claude Code',
      steps: [
        { text: 'Déclarez le serveur :', code: `claude mcp add --transport http ${NAME} ${url}` },
        {
          text: `Dans Claude Code, lancez /mcp, choisissez ${NAME} puis l’authentification : le navigateur s’ouvre sur l’écran d’autorisation.`,
        },
      ],
      note: 'Ajoutez --scope user pour le déclarer dans tous vos projets.',
    },
    {
      id: 'claude',
      label: 'Claude (web et Desktop)',
      steps: [
        {
          text: `Dans Réglages, Connecteurs, ajoutez un connecteur personnalisé nommé ${NAME}, avec cette adresse :`,
          code: url,
        },
        { text: 'Connectez le connecteur : le navigateur s’ouvre sur l’écran d’autorisation.' },
      ],
      note: 'Claude rejoint le serveur depuis Internet : le dashboard doit y être accessible.',
    },
    {
      id: 'codex',
      label: 'Codex',
      steps: [
        { text: 'Déclarez le serveur :', code: `codex mcp add ${NAME} --url ${url}` },
        {
          text: 'Puis autorisez-le : le navigateur s’ouvre sur l’écran d’autorisation.',
          code: `codex mcp login ${NAME}`,
        },
      ],
      note: 'La déclaration, dans ~/.codex/config.toml, sert aussi à l’extension Codex des éditeurs.',
    },
    {
      id: 'gemini',
      label: 'Gemini CLI',
      steps: [
        { text: 'Déclarez le serveur :', code: `gemini mcp add --transport http ${NAME} ${url}` },
        {
          text: 'Dans Gemini CLI, autorisez-le : le navigateur s’ouvre sur l’écran d’autorisation.',
          code: `/mcp auth ${NAME}`,
        },
      ],
      note: 'Ajoutez --scope user pour le déclarer dans tous vos projets.',
    },
    {
      id: 'opencode',
      label: 'opencode',
      steps: [
        { text: 'Déclarez le serveur :', code: `opencode mcp add ${NAME} --url ${url}` },
        {
          text: 'Puis autorisez-le : le navigateur s’ouvre sur l’écran d’autorisation.',
          code: `opencode mcp auth ${NAME}`,
        },
      ],
      note: 'Ajoutez --global pour le déclarer dans tous vos projets.',
    },
    {
      id: 'pi',
      label: 'Pi',
      steps: [
        {
          text: 'Ajoutez le serveur à ~/.pi/agent/mcp.json, ou à .pi/mcp.json dans le projet :',
          code: json({ mcpServers: { [NAME]: { url } } }),
        },
        {
          text: 'Puis autorisez-le, ou dans Pi avec /mcp login : le navigateur s’ouvre sur l’écran d’autorisation.',
          code: `pi mcp login ${NAME}`,
        },
      ],
      note: `Avec l’extension pi-mcp-adapter, la même déclaration va dans ~/.config/mcp/mcp.json, et l’autorisation se lance avec /mcp-auth ${NAME}.`,
    },
    {
      id: 'cursor',
      label: 'Cursor',
      steps: [
        {
          text: 'Ajoutez le serveur à ~/.cursor/mcp.json, ou à .cursor/mcp.json dans le projet :',
          code: json({ mcpServers: { [NAME]: { url } } }),
        },
        {
          text: `Dans les réglages MCP de Cursor, connectez ${NAME} : le navigateur s’ouvre sur l’écran d’autorisation.`,
        },
      ],
    },
    {
      id: 'vscode',
      label: 'VS Code (Copilot)',
      steps: [
        {
          text: 'Ajoutez le serveur à .vscode/mcp.json dans le projet, ou à la configuration MCP de votre profil :',
          code: json({ servers: { [NAME]: { type: 'http', url } } }),
        },
        { text: 'Démarrez le serveur depuis le fichier : VS Code ouvre le navigateur sur l’écran d’autorisation.' },
      ],
    },
    {
      id: 'windsurf',
      label: 'Windsurf',
      steps: [
        {
          text: 'Ajoutez le serveur à ~/.codeium/windsurf/mcp_config.json :',
          code: json({ mcpServers: { [NAME]: { serverUrl: url } } }),
        },
        { text: 'Rafraîchissez les serveurs MCP de Cascade : le navigateur s’ouvre sur l’écran d’autorisation.' },
      ],
    },
  ]
}
