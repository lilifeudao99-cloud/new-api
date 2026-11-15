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
/**
 * LobeHub Icon Loader
 * Dynamically load and render icons from @lobehub/icons
 *
 * Supports:
 * - Basic: "OpenAI", "OpenAI.Color"
 * - Chained properties: "OpenAI.Avatar.type={'platform'}"
 * - Size parameter: getLobeIcon("OpenAI", 20)
 */
import type { ComponentType, ReactNode } from 'react'

import { IconSub2api } from '@/assets/custom/icon-sub2api'
import { DeferredLobeIcon, IconPlaceholder } from '@/components/lobe-icon'

import { lobeIconLoaders, lobeIconNames } from './lobe-icon-loaders.gen'

const CUSTOM_ICONS: Record<string, ComponentType<{ size?: number }>> = {
  Sub2API: IconSub2api,
}

/**
 * Parse a property value from string to appropriate type
 * @param raw - Raw string value
 * @returns Parsed value (boolean, number, or string)
 */
function parseValue(raw: string | undefined | null): string | number | boolean {
  if (raw == null) return true

  let v = String(raw).trim()

  // Remove curly braces
  if (v.startsWith('{') && v.endsWith('}')) {
    v = v.slice(1, -1).trim()
  }

  // Remove quotes
  if (
    (v.startsWith('"') && v.endsWith('"')) ||
    (v.startsWith("'") && v.endsWith("'"))
  ) {
    return v.slice(1, -1)
  }

  // Boolean
  if (v === 'true') return true
  if (v === 'false') return false

  // Number
  if (/^-?\d+(?:\.\d+)?$/.test(v)) return Number(v)

  // Return as string
  return v
}

/**
 * Get LobeHub icon component by name
 * @param iconName - Icon name/description (e.g., "OpenAI", "OpenAI.Color", "Claude.Avatar")
 * @param size - Icon size (default: 20)
 * @returns Icon component or fallback
 *
 * @example
 * getLobeIcon("OpenAI", 24)
 * getLobeIcon("OpenAI.Color", 20)
 * getLobeIcon("Claude.Avatar.type={'platform'}", 32)
 */
export function getLobeIcon(
  iconName: string | undefined | null,
  size: number = 20
): ReactNode {
  const trimmedName = typeof iconName === 'string' ? iconName.trim() : ''
  if (!trimmedName) {
    return <IconPlaceholder name='' size={size} />
  }

  // Parse component path and chained properties
  const segments = trimmedName.split('.')
  const baseKey = segments[0]
  const CustomIcon = Object.hasOwn(CUSTOM_ICONS, baseKey)
    ? CUSTOM_ICONS[baseKey]
    : undefined
  if (CustomIcon) {
    return <CustomIcon size={size} />
  }
  const variants = Object.hasOwn(lobeIconLoaders, baseKey)
    ? lobeIconLoaders[baseKey]
    : undefined
  if (!variants) return <IconPlaceholder name={trimmedName} size={size} />

  let variant = 'default'
  let propStartIndex: number

  if (segments.length > 1 && Object.hasOwn(variants, segments[1])) {
    variant = segments[1]
    propStartIndex = 2
  } else {
    propStartIndex = segments.length > 1 && /^[A-Z]/.test(segments[1]) ? 2 : 1
  }

  // Parse chained properties (e.g., "type={'platform'}", "shape='square'")
  const props: Record<string, string | number | boolean> = {}

  for (let i = propStartIndex; i < segments.length; i++) {
    const seg = segments[i]
    if (!seg) continue

    const eqIdx = seg.indexOf('=')
    if (eqIdx === -1) {
      props[seg.trim()] = true
      continue
    }

    const key = seg.slice(0, eqIdx).trim()
    const valRaw = seg.slice(eqIdx + 1).trim()
    props[key] = parseValue(valRaw)
  }

  // Set size if not explicitly specified in the string
  if (props.size == null && size != null) {
    props.size = size
  }

  return (
    <DeferredLobeIcon
      name={trimmedName}
      iconKey={`${baseKey}.${variant}`}
      loader={variants[variant]}
      iconProps={props}
      size={size}
    />
  )
}

// The selector uses the same installed icon registry as the renderer.
export function getLobeIconNames(): string[] {
  return [...new Set([...lobeIconNames, ...Object.keys(CUSTOM_ICONS)])].sort()
}
