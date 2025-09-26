#!/usr/bin/env node
/* eslint-disable no-console */
const path = require('path');
const concurrently = require('concurrently');

const apps = [
  { name: '@nhb/status-dashboard', label: 'dashboard', color: 'cyan' },
  { name: '@nhb/network-monitor', label: 'monitor', color: 'magenta' }
];

async function run() {
  if (apps.length === 0) {
    console.warn('No applications configured. Update scripts/dev.js to add at least one workspace app.');
    return;
  }

  const commands = apps.map(({ name, label, color }) => ({
    command: `yarn workspace ${name} run dev`,
    name: label,
    prefixColor: color
  }));

  const { result } = concurrently(commands, {
    killOthers: ['failure', 'success'],
    restartTries: 0,
    cwd: path.resolve(__dirname, '..')
  });

  try {
    await result;
  } catch (err) {
    console.error('One of the example applications exited with an error.');
    process.exitCode = 1;
  }
}

run();
