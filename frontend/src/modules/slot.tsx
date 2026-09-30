import { lazy, Suspense, type ComponentType, type LazyExoticComponent } from 'react'
import { Card, ErrorBoundary, Skeleton } from '@/components/ui'

// Widgets and settings sections contributed by feature modules are loaded
// lazily and rendered inside their own error boundary: a module that fails
// to load or throws while rendering costs only its own box.

export interface Reloadable {
  /** Forgets a failed load so the next render loads again. */
  reload?: () => void
}

/** Lazily loads a named export as a component. React.lazy remembers a failed
 *  load for good; `reload` (called by Slot when the user presses "Tekrar
 *  Dene") replaces a failed loader so the retry really loads again. */
export function lazyNamed<M, K extends keyof M, P extends object = object>(
  load: () => Promise<M>,
  name: K,
): ComponentType<P> & Reloadable {
  let failed = false
  const create = (): LazyExoticComponent<ComponentType<P>> =>
    lazy(async () => {
      try {
        const mod = await load()
        return { default: mod[name] as ComponentType<P> }
      } catch (e) {
        failed = true
        throw e
      }
    })
  let current = create()
  function LazyNamed(props: P) {
    const Loaded = current as ComponentType<P>
    return <Loaded {...props} />
  }
  LazyNamed.reload = () => {
    if (!failed) return
    failed = false
    current = create()
  }
  return LazyNamed
}

function Placeholder() {
  return (
    <Card>
      <Skeleton className="mb-4 h-4 w-32" />
      <Skeleton className="mb-2 h-3 w-full" />
      <Skeleton className="mb-2 h-3 w-5/6" />
      <Skeleton className="h-3 w-2/3" />
    </Card>
  )
}

/** Renders a module-provided component in isolation. */
export function Slot({ name, component: Component, bare = false }: { name: string; component: ComponentType & Reloadable; bare?: boolean }) {
  return (
    <ErrorBoundary
      name={name}
      compact
      onRetry={() => Component.reload?.()}
      // The boundary's fallback has no frame of its own.
      key={name}
    >
      <Suspense fallback={bare ? null : <Placeholder />}>
        <Component />
      </Suspense>
    </ErrorBoundary>
  )
}
