import { rm } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, resolve } from 'node:path';

const here = dirname(fileURLToPath(import.meta.url));
await rm(resolve(here, '../../assets/assets'), { force: true, recursive: true });
