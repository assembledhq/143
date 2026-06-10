import { setupServer, type SetupServerApi } from 'msw/node';
import { handlers } from './handlers';

// setup.ts resets the module registry after every test file (see setup.ts),
// which re-evaluates this module for each file in the worker. Keep a single
// MSW server per worker so the fetch/XHR interceptors are patched exactly
// once, instead of a fresh instance re-patching globals on every file.
const serverKey = Symbol.for('143-tests/msw-server');
const holder = globalThis as { [serverKey]?: SetupServerApi };

export const server: SetupServerApi = (holder[serverKey] ??= setupServer(...handlers));
