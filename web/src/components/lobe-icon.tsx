/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
import { createElement, useEffect, useState, type ComponentType } from 'react'

type IconComponent = ComponentType<Record<string, unknown>>
const loadedIcons = new Map<string, IconComponent>()
const pendingIcons = new Map<string, Promise<IconComponent | null>>()

export function IconPlaceholder(props: { name: string; size: number }) {
  return (
    <div
      className='bg-muted text-muted-foreground flex items-center justify-center rounded-full text-xs font-medium'
      style={{ width: props.size, height: props.size }}
    >
      {props.name.charAt(0).toUpperCase() || '?'}
    </div>
  )
}

export function DeferredLobeIcon(props: {
  name: string
  iconKey: string
  loader: () => Promise<{ default: unknown }>
  iconProps: Record<string, string | number | boolean>
  size: number
}) {
  const iconKey = props.iconKey
  const loader = props.loader
  const [loaded, setLoaded] = useState<{
    key: string
    component: IconComponent
  } | null>(null)
  const component =
    loaded?.key === iconKey ? loaded.component : loadedIcons.get(iconKey)

  useEffect(() => {
    let active = true
    let pending = pendingIcons.get(iconKey)
    if (!pending) {
      pending = loader().then(
        (module) => {
          const component = module.default as IconComponent
          loadedIcons.set(iconKey, component)
          return component
        },
        // An unavailable icon chunk must never break the surrounding page.
        () => {
          pendingIcons.delete(iconKey)
          return null
        }
      )
      pendingIcons.set(iconKey, pending)
    }
    void pending.then((component) => {
      if (active && component) setLoaded({ key: iconKey, component })
    })
    return () => {
      active = false
    }
  }, [iconKey, loader])

  if (!component) return <IconPlaceholder name={props.name} size={props.size} />
  return createElement(component, props.iconProps)
}
