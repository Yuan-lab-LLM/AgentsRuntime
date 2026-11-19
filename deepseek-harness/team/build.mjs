import fs from 'node:fs/promises';
import path from 'node:path';
import { buildProtocol } from '../../opencode/team/build-protocol.mjs';
const output = process.argv[2];
if (!output) throw Error('DSH protocol output required');
await buildProtocol(path.resolve(output));
await fs.appendFile(output, '\nexport { teamSendParameters, teamStatusParameters, progressParameters, completeParameters, artifactWriteParameters, artifactReadParameters, artifactListParameters, artifactMkdirParameters, artifactPreviewParameters };\n');
