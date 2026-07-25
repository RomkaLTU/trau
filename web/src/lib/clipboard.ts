// The hub is reached over plain HTTP on a LAN or tailnet address, which is not a
// secure context, so navigator.clipboard is simply absent there and the legacy
// selection path is the only one that copies anything.
export async function copyText(text: string): Promise<boolean> {
  if (navigator.clipboard) {
    try {
      await navigator.clipboard.writeText(text)
      return true
    } catch {
      return copyViaSelection(text)
    }
  }
  return copyViaSelection(text)
}

function copyViaSelection(text: string): boolean {
  const restore = document.activeElement as HTMLElement | null
  const area = document.createElement('textarea')
  area.value = text
  area.setAttribute('readonly', '')
  area.style.position = 'fixed'
  area.style.opacity = '0'
  document.body.append(area)
  area.select()
  try {
    return document.execCommand('copy')
  } catch {
    return false
  } finally {
    area.remove()
    restore?.focus()
  }
}
