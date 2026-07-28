import { describe, expect, it } from 'vitest'

import { breadcrumbs } from './fs-browse'

describe('breadcrumbs', () => {
  it('is empty on the roots listing', () => {
    expect(breadcrumbs('', '/')).toEqual([])
  })

  it('walks a posix path from its filesystem root', () => {
    expect(breadcrumbs('/Users/you/Projects', '/')).toEqual([
      { label: '/', path: '/' },
      { label: 'Users', path: '/Users' },
      { label: 'you', path: '/Users/you' },
      { label: 'Projects', path: '/Users/you/Projects' },
    ])
  })

  it('keeps the posix root itself to a single hop', () => {
    expect(breadcrumbs('/', '/')).toEqual([{ label: '/', path: '/' }])
  })

  it('keeps a windows volume prefix whole', () => {
    expect(breadcrumbs('C:\\Users\\you\\Projects', '\\')).toEqual([
      { label: 'C:\\', path: 'C:\\' },
      { label: 'Users', path: 'C:\\Users' },
      { label: 'you', path: 'C:\\Users\\you' },
      { label: 'Projects', path: 'C:\\Users\\you\\Projects' },
    ])
  })

  it('keeps a windows drive root to a single hop', () => {
    expect(breadcrumbs('D:\\', '\\')).toEqual([{ label: 'D:\\', path: 'D:\\' }])
  })
})
