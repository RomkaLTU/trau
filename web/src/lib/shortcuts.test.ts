import { describe, expect, it } from 'vitest'

import { G_TARGETS, G_TARGET_KEYS, gTarget } from '@/components/trau/nav-items'

import { SHORTCUTS, keyLabel, shortcutSections } from './shortcuts'

describe('G_TARGETS', () => {
  it('binds every g key to a nav item', () => {
    expect(G_TARGETS.map((target) => target.key)).toEqual([
      'o',
      'l',
      'b',
      'i',
      'e',
      'r',
      't',
      'a',
      'c',
      'n',
      'k',
      's',
      'h',
    ])
    expect(gTarget('b')?.to).toBe('/backlog')
    expect(gTarget('o')?.to).toBe('/')
    expect(gTarget('h')?.to).toBe('/hub')
    expect(gTarget('z')).toBeNull()
  })

  it('keeps the project gate the nav item declares', () => {
    expect(gTarget('b')?.requiresProject).toBe(true)
    expect(gTarget('r')?.requiresProject).toBeUndefined()
  })

  it('exposes the keys the navigator resolves against', () => {
    expect(G_TARGET_KEYS.size).toBe(G_TARGETS.length)
    expect(G_TARGET_KEYS.has('b')).toBe(true)
    expect(G_TARGET_KEYS.has('g')).toBe(false)
  })
})

describe('SHORTCUTS', () => {
  it('registers the seeded bindings', () => {
    const labels = SHORTCUTS.map((shortcut) => shortcut.label)
    expect(labels).toContain('Open the command palette')
    expect(labels).toContain('Switch project')
    expect(labels).toContain('Show keyboard shortcuts')
    expect(labels).toContain('Send the message')
    expect(labels).toContain("Open the highlighted ticket's actions")
  })

  it('registers the inbox walk and its composer key', () => {
    const inbox = SHORTCUTS.filter((shortcut) => shortcut.where === 'Inbox')
    expect(inbox.map((shortcut) => shortcut.keys[0])).toEqual([
      '↓',
      '↑',
      'Home',
      'End',
      'Enter',
      'Tab',
    ])
    expect(inbox[0].alt).toEqual(['j'])
    expect(inbox[1].alt).toEqual(['k'])
    expect(inbox[4].label).toBe('Open the selected issue')
    expect(
      SHORTCUTS.filter((shortcut) => shortcut.where === 'Inbox composer'),
    ).toEqual([
      {
        keys: ['Enter'],
        label: 'Send the message',
        group: 'Lists',
        where: 'Inbox composer',
      },
    ])
  })

  it('registers every backlog row binding', () => {
    const backlog = SHORTCUTS.filter((shortcut) => shortcut.where === 'Backlog')
    expect(backlog.map((shortcut) => shortcut.keys[0])).toEqual([
      '↓',
      '↑',
      'Home',
      'End',
      'Enter',
      'Tab',
      'e',
      'a',
    ])
    expect(backlog[0].alt).toEqual(['j'])
    expect(backlog[1].alt).toEqual(['k'])
  })

  it('registers every loop row binding', () => {
    const loop = SHORTCUTS.filter((shortcut) => shortcut.where === 'Loop')
    expect(loop.map((shortcut) => shortcut.keys)).toEqual([
      ['↓'],
      ['↑'],
      ['Home'],
      ['End'],
      ['Enter'],
      ['Tab'],
      ['Space'],
      ['Alt', '↑'],
      ['Alt', '↓'],
      ['r'],
      ['x'],
    ])
    expect(loop[0].alt).toEqual(['j'])
    expect(loop[1].alt).toEqual(['k'])
    expect(loop.map((shortcut) => shortcut.label).slice(6)).toEqual([
      'Include the row in a batch',
      'Move the row up the queue',
      'Move the row down the queue',
      'Run the row now',
      'Remove the row from the queue',
    ])
  })

  it('registers the runs walk, which has no row actions to step into', () => {
    const runs = SHORTCUTS.filter((shortcut) => shortcut.where === 'Runs')
    expect(runs.map((shortcut) => shortcut.keys[0])).toEqual([
      '↓',
      '↑',
      'Home',
      'End',
      'Enter',
    ])
    expect(runs[0].alt).toEqual(['j'])
    expect(runs[1].alt).toEqual(['k'])
    expect(runs[4].label).toBe('Open the run')
  })

  it('registers the drawer walk beside the inbox one', () => {
    const drawer = SHORTCUTS.filter(
      (shortcut) => shortcut.where === 'Ticket drawer',
    )
    expect(drawer.map((shortcut) => shortcut.keys)).toEqual([['j'], ['k']])
    expect(drawer.map((shortcut) => shortcut.label)).toEqual([
      'Next ticket',
      'Previous ticket',
    ])
  })

  it('registers one navigation binding per g target', () => {
    const nav = SHORTCUTS.filter((shortcut) => shortcut.group === 'Navigation')
    expect(nav).toHaveLength(G_TARGETS.length)
    expect(nav.map((shortcut) => shortcut.keys)).toContainEqual(['g', 'b'])
    expect(nav[2].label).toBe('Go to Backlog')
  })
})

describe('shortcutSections', () => {
  it('buckets the registry in group order', () => {
    expect(shortcutSections().map((section) => section.group)).toEqual([
      'Global',
      'Navigation',
      'Lists',
      'Palette',
    ])
  })

  it('holds every registered binding once', () => {
    const rows = shortcutSections().flatMap((section) => section.items)
    expect(rows).toHaveLength(SHORTCUTS.length)
  })

  it('drops a group nothing is registered under', () => {
    const only = shortcutSections([
      { keys: ['j'], label: 'Next issue', group: 'Lists', where: 'Inbox' },
    ])
    expect(only.map((section) => section.group)).toEqual(['Lists'])
  })
})

describe('keyLabel', () => {
  it('renders the platform modifier', () => {
    expect(keyLabel('mod', true)).toBe('⌘')
    expect(keyLabel('mod', false)).toBe('Ctrl')
  })

  it('leaves a plain key alone', () => {
    expect(keyLabel('K', true)).toBe('K')
    expect(keyLabel('g', false)).toBe('g')
  })
})
