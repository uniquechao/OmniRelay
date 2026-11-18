(() => {
  const stage = document.querySelector('.login-brand');
  const orbit = stage.querySelector('.login-orbit');
  const canvas = stage.querySelector('.login-motion');
  const context = canvas.getContext('2d');
  if (!context) return;

  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  const fullTurn = 2 * Math.PI;
  const fraction = value => value - Math.floor(value);
  const palette = getComputedStyle(stage);
  const indigo = palette.getPropertyValue('--or-indigo-300').trim();
  const teal = palette.getPropertyValue('--or-teal-300').trim();
  const rings = [
    { radius:.320, speed:.32, phase:.35, color:indigo, opacity:.75 },
    { radius:.382, speed:-.27, phase:1.9, color:teal, opacity:.66 },
    { radius:.446, speed:.22, phase:3.5, color:indigo, opacity:.55 },
    { radius:.510, speed:-.17, phase:5.1, color:teal, opacity:.48 }
  ];
  const packets = Array.from({ length:34 }, (_,index) => ({
    angle:fullTurn * fraction(index * .618034),
    speed:30 + index % 5 * 3,
    offset:fraction(index * .414214),
    color:index % 2 === 0 ? teal : indigo,
    text:Array.from({ length:3 + index % 4 }, (_,glyphIndex) => {
      const sample = Math.sin((index + 1) * 75.81 + glyphIndex * 34.17) * 847.31;
      return fraction(sample) < .5 ? '0' : '1';
    }).join('')
  }));

  let width = 0;
  let height = 0;
  let scale = 0;
  let centerX = 0;
  let centerY = 0;
  let frame = 0;
  let elapsed = 0;
  let previousTime;

  function drawStreams(time) {
    const compact = scale <= 140;
    const fontSize = compact ? 9 : 11;
    context.textAlign = 'center';
    context.textBaseline = 'middle';
    context.font = '500 ' + fontSize + 'px ui-monospace, Menlo, monospace';
    const count = compact ? 19 : packets.length;
    for (let index = 0; index < count; index += 1) {
      const packet = packets[index];
      const directionX = Math.cos(packet.angle);
      const directionY = Math.sin(packet.angle);
      const roomX = directionX < 0 ? centerX : width - centerX;
      const roomY = directionY < 0 ? centerY : height - centerY;
      const edge = Math.min(
        (roomX + 12) / Math.max(.001,Math.abs(directionX)),
        (roomY + 12) / Math.max(.001,Math.abs(directionY))
      );
      const length = edge - scale * .28;
      if (length <= 0) continue;
      const progress = fraction(packet.offset + time * packet.speed / length);
      const distance = edge - progress * length;
      const fade = Math.min(1,progress * 5,(1 - progress) * 5);
      context.save();
      context.translate(directionX * distance,directionY * distance);
      context.rotate(packet.angle + Math.PI);
      context.fillStyle = packet.color;
      context.globalAlpha = fade * .56;
      context.fillText(packet.text,0,0);
      context.restore();
    }
  }

  function drawRing(ring,time) {
    const { phase, speed, radius, color, opacity } = ring;
    const rotation = phase + time * speed;
    const swell = Math.pow(.5 + .5 * Math.sin(time * 3.7 + phase),3);
    context.globalAlpha = opacity * (.82 + .18 * swell);
    context.strokeStyle = color;
    context.lineWidth = scale <= 140 ? 1 : 1.25;
    context.beginPath();
    for (let step = 0; step < 120; step += 1) {
      const angle = fullTurn * step / 120;
      const ripple = Math.sin(angle * 3 + phase + time * 1.6) * .014 +
        Math.sin(angle * 7 - time * 2.5 + phase) * (.003 + .009 * swell) +
        Math.sin(angle * 11 + time * 3.2) * (.001 + .003 * swell);
      const distance = scale * (radius + .008 * swell + ripple);
      context[step === 0 ? 'moveTo' : 'lineTo'](
        Math.cos(angle + rotation) * distance,Math.sin(angle + rotation) * distance
      );
    }
    context.closePath();
    context.stroke();
  }

  function draw(time) {
    context.clearRect(0,0,width,height);
    context.save();
    context.translate(centerX,centerY);
    context.lineCap = 'round';
    drawStreams(time);
    rings.forEach(ring => drawRing(ring,time));
    context.restore();
  }

  function tick(now) {
    if (previousTime !== undefined) elapsed += Math.min(now - previousTime,50) / 1000;
    previousTime = now;
    draw(elapsed);
    frame = requestAnimationFrame(tick);
  }

  function syncMotion() {
    cancelAnimationFrame(frame);
    previousTime = undefined;
    const bounds = stage.getBoundingClientRect();
    ({ width,height } = bounds);
    if (!width || !height || document.hidden) return;

    const orbitBounds = orbit.getBoundingClientRect();
    scale = orbitBounds.width;
    centerX = orbitBounds.left - bounds.left + scale / 2;
    centerY = orbitBounds.top - bounds.top + orbitBounds.height / 2;
    const density = Math.min(devicePixelRatio || 1,2);
    canvas.width = Math.round(width * density);
    canvas.height = Math.round(height * density);
    context.setTransform(density,0,0,density,0,0);
    draw(reducedMotion.matches ? 0 : elapsed);
    stage.dataset.motionReady = '';
    if (!reducedMotion.matches) frame = requestAnimationFrame(tick);
  }

  new ResizeObserver(syncMotion).observe(stage);
  document.addEventListener('visibilitychange',syncMotion);
  reducedMotion.addEventListener('change',syncMotion);
})();
