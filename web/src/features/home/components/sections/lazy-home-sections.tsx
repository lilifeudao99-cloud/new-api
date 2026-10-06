/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
*/
import { Footer } from '@/components/layout/components/footer'

import { CTA } from './cta'
import { Features } from './features'
import { HowItWorks } from './how-it-works'
import { Stats } from './stats'

type LazyHomeSectionsProps = {
  isAuthenticated: boolean
}

export function LazyHomeSections(props: LazyHomeSectionsProps) {
  return (
    <>
      <Stats />
      <Features />
      <HowItWorks />
      <CTA isAuthenticated={props.isAuthenticated} />
      <Footer />
    </>
  )
}
