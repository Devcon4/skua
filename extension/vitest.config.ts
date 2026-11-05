import {defineConfig} from 'vitest/config';

export default defineConfig({
  test: {
    sequence: {concurrent: true},
    projects: [
      {
        extends: true,
        test: {
          name: 'unit',
          include: ['src/**/*.spec.ts', 'test/fakes/**/*.spec.ts'],
        },
      },
      {extends: true, test: {name: 'rpc', include: ['test/rpc/**/*.test.ts']}},
      {
        extends: true,
        test: {
          name: 'kind',
          include: ['test/kind/**/*.test.ts'],
          sequence: {concurrent: false},
        },
      },
    ],
  },
});
