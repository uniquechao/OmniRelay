import { z } from 'zod'

// Production pages apply script-src 'self'. Use Zod's interpreter without probing eval.
z.config({ jitless: true })
export { z }
