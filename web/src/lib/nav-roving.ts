import {
  NAV_GROUPS,
  type NavGroup,
  type NavItem,
} from '@/components/trau/nav-items'

export function navOrder(groups: readonly NavGroup[] = NAV_GROUPS): NavItem[] {
  return groups.flatMap((group) => group.items)
}

export function rovingIndex(
  key: string,
  current: number,
  count: number,
): number | null {
  if (count <= 0) return null
  const at = Math.min(Math.max(current, 0), count - 1)
  switch (key) {
    case 'ArrowDown':
      return Math.min(at + 1, count - 1)
    case 'ArrowUp':
      return Math.max(at - 1, 0)
    case 'Home':
      return 0
    case 'End':
      return count - 1
    default:
      return null
  }
}

export function activeNavIndex(
  pathname: string,
  items: readonly NavItem[] = navOrder(),
): number {
  const found = items.findIndex((item) =>
    item.exact
      ? pathname === item.to
      : pathname === item.to || pathname.startsWith(`${item.to}/`),
  )
  return found === -1 ? 0 : found
}
