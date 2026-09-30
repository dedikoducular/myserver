import { expect } from 'vitest'

/** Accessibility smoke check for whatever is currently rendered: every form
 *  control and every button (icon-only ones included) has an accessible name. */
export function expectAccessibleNames(root: ParentNode = document.body): void {
  const controls = Array.from(root.querySelectorAll<HTMLElement>('input:not([type="hidden"]), select, textarea'))
  for (const el of controls) {
    expect(el, `etiketsiz alan: ${el.outerHTML.slice(0, 160)}`).toHaveAccessibleName()
  }
  const buttons = Array.from(root.querySelectorAll<HTMLElement>('button, [role="button"], a[href]'))
  for (const el of buttons) {
    expect(el, `adsız düğme: ${el.outerHTML.slice(0, 160)}`).toHaveAccessibleName()
  }
}
