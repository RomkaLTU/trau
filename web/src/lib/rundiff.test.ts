import { describe, expect, it } from 'vitest'

import { firstChangedLine } from './rundiff'

describe('firstChangedLine', () => {
  it('reads the new-file start of the first hunk', () => {
    const patch = [
      '@@ -12,7 +18,9 @@ func main() {',
      ' 	ctx := context.Background()',
      '+	log.Print("hi")',
      '@@ -90,2 +99,2 @@',
      '-	old()',
      '+	next()',
    ].join('\n')
    expect(firstChangedLine(patch)).toBe(18)
  })

  it('handles a single-line hunk header', () => {
    expect(firstChangedLine('@@ -0,0 +1 @@\n+first\n')).toBe(1)
  })

  it('returns nothing when the patch carries no hunk header', () => {
    expect(firstChangedLine('')).toBeUndefined()
  })
})
