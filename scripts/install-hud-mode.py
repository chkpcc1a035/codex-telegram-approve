#!/usr/bin/env python3
"""Add the approval mode indicator to an existing codex-hud checkout."""
import argparse
from pathlib import Path

MODULE = '''/** Approval handler for the next request; shared with the Telegram hook. */
import { readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { join } from 'node:path';

export function getApprovalModeDisplay(): string {
  try {
    const config = JSON.parse(readFileSync(join(homedir(), '.config',
      'codex-telegram-approve', 'mode.json'), 'utf8'));
    if (config.mode === 'computer') return 'Computer';
    if (config.mode === 'telegram') return 'Telegram';
    if (config.mode === 'concurrent') return 'Concurrent (terminal priority)';
    return 'Unavailable (requests denied)';
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code === 'ENOENT') return 'Computer';
    return 'Unavailable (requests denied)';
  }
}
'''

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('hud_root', type=Path)
    args = parser.parse_args()
    root = args.hud_root.resolve()
    renderer = root / 'src/render/lines/environment-line.ts'
    source = renderer.read_text()
    import_line = "import { getApprovalModeDisplay } from '../../collectors/approval-mode.js';\n"
    anchor = "  parts.push(colors.dim('Approval: ') + theme.value(approvalPolicy));"
    indicator = "\n  parts.push(colors.dim('Approval mode (next): ') + theme.value(getApprovalModeDisplay()));"
    if anchor not in source:
        parser.error('Unsupported HUD renderer; no files changed.')
    if import_line not in source:
        source = import_line + source
    if indicator not in source:
        source = source.replace(anchor, anchor + indicator, 1)
    module = root / 'src/collectors/approval-mode.ts'
    legacy_module = MODULE.replace("    if (config.mode === 'concurrent') return 'Concurrent (terminal priority)';\n", '')
    if module.exists() and module.read_text() not in (MODULE, legacy_module):
        parser.error('Existing approval-mode.ts differs; no files changed.')
    module.write_text(MODULE)
    renderer.write_text(source)
    print('HUD indicator installed. Run npm run build in ' + str(root) + ', then restart cx.')

if __name__ == '__main__':
    main()
