import postcss, { AtRule, type Rule } from 'postcss'

/*
 * La feuille d'un plugin, rangée pour ne jamais défaire celle du noyau.
 *
 * Les deux feuilles sont produites par Tailwind, chacune avec ses utilitaires de
 * base puis ses variantes, dans la couche `utilities`. Chargée après, celle du
 * plugin y reprenait le dessus : son `.flex` passait après le `.sm:table-row` du
 * noyau et l'annulait, sur des écrans qui ne doivent rien au plugin.
 *
 * Ses utilitaires de base passent donc dans la couche `components`, sous tous ceux
 * du noyau ; ses variantes restent dans `utilities`, après celles du noyau, pour
 * que `hidden lg:flex` écrit par un plugin s'applique même quand `.hidden` vient du
 * noyau. Il reste un cas : deux variantes de seuils différents, l'une de chaque
 * feuille, sur un même élément ; celle du plugin l'emporte.
 */

/** Une règle de variante : son sélecteur porte le préfixe échappé d'une variante
 *  (`.sm\:flex`, `.hover\:bg-white:hover`). */
function isVariant(rule: Rule): boolean {
  return rule.selector.includes('\\:')
}

export function layerPluginStyles(css: string): string {
  const root = postcss.parse(css)
  root.walkAtRules('layer', (layer) => {
    if (layer.params.trim() !== 'utilities' || !layer.nodes) return
    const base = new AtRule({ name: 'layer', params: 'components' })
    for (const node of [...layer.nodes]) {
      // Une règle-média est une variante : elle reste où elle est.
      if (node.type === 'rule' && !isVariant(node)) base.append(node.remove())
    }
    if (base.nodes && base.nodes.length > 0) layer.before(base)
  })
  return root.toString()
}
