/*
 * Copie dans le presse-papiers.
 *
 * `navigator.clipboard` n'existe qu'en **contexte sécurisé** : HTTPS, ou
 * `localhost`. Le dashboard est servi en clair sur son propre nom d'hôte —
 * `http://dashboard.mse.local` en local, et rien ne garantit le contraire
 * ailleurs — donc l'objet y est purement absent. Lire `.writeText` dessus lève
 * une `TypeError` **avant** toute promesse, ce qu'un `.catch()` ne rattrape pas :
 * le bouton ne copiait rien et ne le disait pas.
 *
 * D'où le repli sur `document.execCommand('copy')`. Il est déprécié, et c'est
 * assumé : il reste la seule voie hors contexte sécurisé, et la seule alternative
 * serait de renoncer au bouton là où il sert.
 *
 * Le résultat est rendu à l'appelant, et non passé sous silence : une copie qui
 * échoue sans le dire fait coller autre chose sans qu'on s'en aperçoive.
 */
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard?.writeText) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      // Permission refusée, ou onglet sans focus. Le repli peut encore passer.
    }
  }

  const field = document.createElement('textarea')
  field.value = text
  // `readonly` évite le clavier virtuel sur mobile ; la sélection reste possible.
  field.setAttribute('readonly', '')
  // Hors de vue mais dans la page : un élément `display: none` ne peut pas être
  // sélectionné, et c'est la sélection qui porte la copie.
  field.style.position = 'fixed'
  field.style.top = '0'
  field.style.opacity = '0'
  field.style.pointerEvents = 'none'
  document.body.appendChild(field)

  try {
    field.select()
    // `select()` seul ne suffit pas sur iOS, où la plage doit être posée.
    field.setSelectionRange(0, text.length)
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    field.remove()
  }
}
