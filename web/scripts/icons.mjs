// Draws the app icon (a white check mark on a green rounded square) as PNG
// files, without any image library: node scripts/icons.mjs
import { writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { crc32, deflateSync } from 'node:zlib';

const output = join(dirname(fileURLToPath(import.meta.url)), '..', 'public');
const background = [15, 118, 71];
const samplesPerAxis = 4;

// Distance from a point to the line segment between two points.
function distanceToSegment(x, y, [startX, startY], [endX, endY]) {
  const deltaX = endX - startX;
  const deltaY = endY - startY;
  const along = Math.max(0, Math.min(1, ((x - startX) * deltaX + (y - startY) * deltaY) / (deltaX * deltaX + deltaY * deltaY)));
  return Math.hypot(x - (startX + along * deltaX), y - (startY + along * deltaY));
}

// What lies at a point of the unit square: nothing, background or the mark.
function colorAt(x, y, margin) {
  const radius = 0.2;
  const low = margin;
  const high = 1 - margin;
  const nearestX = Math.max(low + radius, Math.min(high - radius, x));
  const nearestY = Math.max(low + radius, Math.min(high - radius, y));
  if (Math.hypot(x - nearestX, y - nearestY) > radius) {
    return null;
  }
  const elbow = [0.44, 0.66];
  const onMark = Math.min(distanceToSegment(x, y, [0.29, 0.52], elbow), distanceToSegment(x, y, elbow, [0.72, 0.36])) < 0.055;
  return onMark ? [255, 255, 255] : background;
}

function chunk(type, data) {
  const body = Buffer.concat([Buffer.from(type), data]);
  const length = Buffer.alloc(4);
  length.writeUInt32BE(data.length);
  const checksum = Buffer.alloc(4);
  checksum.writeUInt32BE(crc32(body));
  return Buffer.concat([length, body, checksum]);
}

function png(size, margin) {
  const rows = Buffer.alloc(size * (size * 4 + 1));
  for (let row = 0; row < size; row++) {
    for (let column = 0; column < size; column++) {
      const sum = [0, 0, 0, 0];
      for (let sampleY = 0; sampleY < samplesPerAxis; sampleY++) {
        for (let sampleX = 0; sampleX < samplesPerAxis; sampleX++) {
          const color = colorAt((column + (sampleX + 0.5) / samplesPerAxis) / size, (row + (sampleY + 0.5) / samplesPerAxis) / size, margin);
          if (color) {
            sum[0] += color[0];
            sum[1] += color[1];
            sum[2] += color[2];
            sum[3] += 1;
          }
        }
      }
      const offset = row * (size * 4 + 1) + 1 + column * 4;
      const covered = Math.max(sum[3], 1);
      rows[offset] = sum[0] / covered;
      rows[offset + 1] = sum[1] / covered;
      rows[offset + 2] = sum[2] / covered;
      rows[offset + 3] = (sum[3] * 255) / (samplesPerAxis * samplesPerAxis);
    }
  }
  const header = Buffer.alloc(13);
  header.writeUInt32BE(size, 0);
  header.writeUInt32BE(size, 4);
  header.set([8, 6, 0, 0, 0], 8); // 8 bit, RGBA
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', header),
    chunk('IDAT', deflateSync(rows, { level: 9 })),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

writeFileSync(join(output, 'icon-192.png'), png(192, 0.04));
writeFileSync(join(output, 'icon-512.png'), png(512, 0.04));
// "Maskable" icons are cropped by the system, so the square fills the image.
writeFileSync(join(output, 'icon-maskable-512.png'), png(512, -0.2));
