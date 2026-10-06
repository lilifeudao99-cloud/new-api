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
import i18n from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'

import { convertDetectedLanguage } from './languages'
import zhCN from './locales/zh.json'

export const resources = {
  zhCN,
} as const

type SupportedLanguage =
  | keyof typeof resources
  | 'en'
  | 'fr'
  | 'ru'
  | 'ja'
  | 'vi'
  | 'zhTW'
type TranslationResource = { translation: Record<string, unknown> }

// Keep the default language in the entry chunk. Other language packs are
// loaded only after the user selects them, so the public home page stays
// Chinese without paying for translations it will never display.
const localeLoaders: Record<
  Exclude<SupportedLanguage, 'zhCN'>,
  () => Promise<{ default: TranslationResource }>
> = {
  en: () => import('./locales/en.json'),
  fr: () => import('./locales/fr.json'),
  ru: () => import('./locales/ru.json'),
  ja: () => import('./locales/ja.json'),
  vi: () => import('./locales/vi.json'),
  zhTW: () => import('./locales/zh-TW.json'),
}

const localePromises = new Map<string, Promise<void>>()

async function loadLocale(language: string) {
  const locale = language as Exclude<SupportedLanguage, 'zhCN'>
  const loader = localeLoaders[locale]
  if (!loader || i18n.hasResourceBundle(locale, 'translation')) return

  const existing = localePromises.get(locale)
  if (existing) return existing

  const promise = loader()
    .then(({ default: resource }) => {
      i18n.addResourceBundle(
        locale,
        'translation',
        resource.translation,
        true,
        true
      )
    })
    .then(async () => {
      // A languageChanged event fires before this async chunk resolves. Repeat
      // the change once the bundle is present so react-i18next re-renders with
      // the newly loaded translations.
      if (i18n.language === locale) await i18n.changeLanguage(locale)
    })
  localePromises.set(locale, promise)
  return promise
}

i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources,
    fallbackLng: 'zhCN',
    supportedLngs: ['en', 'zhCN', 'fr', 'ru', 'ja', 'vi', 'zhTW'],
    load: 'currentOnly',
    nsSeparator: false, // Allow literal colons in keys (e.g., URLs, labels)
    debug: import.meta.env.DEV,
    interpolation: {
      escapeValue: false, // not needed for react as it escapes by default
    },
    detection: {
      // Chinese is the product default. A manually selected language is
      // persisted in localStorage and takes precedence on later visits.
      order: ['localStorage'],
      caches: ['localStorage'],
      // Browsers report `zh-CN`/`zh-TW`/`zh`; map them onto our `zhCN`/`zhTW`
      // codes (non-Chinese codes pass through for normal supportedLngs matching).
      convertDetectedLanguage,
    },
  })
  .then(() => loadLocale(i18n.resolvedLanguage ?? i18n.language))
  .catch(() => {
    // Keep the Chinese fallback usable if an optional locale chunk fails.
  })

// The default Chinese bundle is available immediately. When a user selects a
// different language, fetch that language pack and re-apply the language after
// it arrives so react-i18next re-renders with the loaded translations.
i18n.on('languageChanged', (language) => {
  void loadLocale(language)
})

export default i18n
