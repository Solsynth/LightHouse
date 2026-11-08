/**
 * The module IDs Stargate reports through GET /api/modules. The console gates
 * its surfaces on these instead of guessing which backend is compiled in.
 */
export const MODULES = {
  auth: 'app.solian.id.auth',
  accounts: 'app.solian.id.accounts',
  profiles: 'app.solian.id.profiles',
  keys: 'app.solian.id.keys',
  social: 'app.solian.id.social',
  relationships: 'app.solian.id.relationships',
  tickets: 'app.solian.id.tickets',
  admin: 'app.solian.admin',
} as const
