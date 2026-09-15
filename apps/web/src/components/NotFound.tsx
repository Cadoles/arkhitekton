import { Link, useLocation } from 'react-router'
import { Icon } from './Icon'
import { Card } from './primitives'

export function NotFound() {
  const location = useLocation()

  return (
    <Card title="Page introuvable">
      <p className="text-sm text-zinc-600">
        Aucun écran ne correspond à <code className="font-mono text-zinc-900">{location.pathname}</code>.
      </p>
      <Link
        to="/"
        className="mt-4 inline-flex items-center gap-2 text-sm font-medium text-zinc-900 hover:underline"
      >
        Revenir à l&apos;accueil
        <Icon name="arrow-right" className="size-4" />
      </Link>
    </Card>
  )
}
