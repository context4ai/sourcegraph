// The index field: a dot matrix where each dot stands for a small batch of files.
// States: idle -> typing (ripples, candidates narrow) -> scan (live sweep accelerates) -> settled (hits stay lit).
(function () {
  var PITCH = 9, R = 1.45, SHARD_COLS = 12, LEVELS = 24;

  function hash(str) {
    var h = 2166136261;
    for (var i = 0; i < str.length; i++) { h ^= str.charCodeAt(i); h = Math.imul(h, 16777619); }
    return h >>> 0;
  }
  function rand(i, seed) {
    var x = Math.imul(i ^ seed, 2654435761) ^ (seed >>> 13);
    x = Math.imul(x ^ (x >>> 15), 2246822519);
    x ^= x >>> 13;
    return (x >>> 0) / 4294967296;
  }

  function Field(canvas, opts) {
    this.c = canvas;
    this.ctx = canvas.getContext('2d');
    this.opts = opts || {};
    this.reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
    this.mode = 'idle';
    this.energy = 0;
    this.seed = 0;
    this.frac = 0;
    this.ripples = [];
    this.sweep = 0; this.speed = 0; this.passes = 0;
    this.flashAt = -1e9;
    this.focusKey = null;
    this.files = {};
    this.labels = [];
    this.readColors();
    this.layout();
    var self = this;
    new ResizeObserver(function () { self.layout(); }).observe(canvas);
    new MutationObserver(function () { self.readColors(); }).observe(document.documentElement, { attributes: true, attributeFilter: ['class'] });
    this.last = performance.now();
    requestAnimationFrame(function loop(now) { self.frame(now); requestAnimationFrame(loop); });
    // Settled hits with an href open that file; the results list carries the same links for keyboard use.
    this.hoverKey = null;
    this.labelBoxes = [];
    canvas.addEventListener('mousemove', function (e) {
      var f = self.pick(e);
      canvas.style.cursor = f && f.href ? 'pointer' : '';
      var key = f ? f.key : null;
      if (key !== self.hoverKey) { self.hoverKey = key; self.focus(key); }
    });
    canvas.addEventListener('mouseleave', function () {
      canvas.style.cursor = '';
      if (self.hoverKey) { self.hoverKey = null; self.focus(null); }
    });
    canvas.addEventListener('click', function (e) {
      var f = self.pick(e);
      if (!f || !f.href) return;
      if (e.metaKey || e.ctrlKey) window.open(f.href, '_blank', 'noopener');
      else location.href = f.href;
    });
  }

  // The settled file under the pointer: its label, or the hit cluster nearest to it.
  Field.prototype.pick = function (e) {
    if (this.mode !== 'settled' || !this.lastFiles || !this.n) return null;
    var r = this.c.getBoundingClientRect(), x = e.clientX - r.left, y = e.clientY - r.top, files = this.lastFiles;
    for (var b = 0; b < this.labelBoxes.length; b++) {
      var lb = this.labelBoxes[b];
      if (x >= lb.box[0] && x <= lb.box[2] && y >= lb.box[1] && y <= lb.box[3]) return files.filter(function (f) { return f.key === lb.key; })[0] || null;
    }
    var best = -1, bestD = PITCH * PITCH, now = performance.now();
    for (var i = 0; i < this.n; i++) {
      if (this.owner[i] < 0 || !this.hit[i] || now < this.hit[i]) continue;
      var dx = this.x[i] - x, dy = this.y[i] - y, d = dx * dx + dy * dy;
      if (d < bestD) { bestD = d; best = i; }
    }
    return best >= 0 ? files[this.owner[best]] || null : null;
  };

  Field.prototype.readColors = function () {
    var cs = getComputedStyle(document.documentElement);
    this.inkColor = 'oklch(' + cs.getPropertyValue('--ink').trim() + ')';
    this.liveColor = 'oklch(' + cs.getPropertyValue('--live').trim() + ')';
    this.dark = document.documentElement.classList.contains('dark');
  };

  Field.prototype.layout = function () {
    var dpr = Math.min(window.devicePixelRatio || 1, 2);
    var w = this.c.clientWidth, h = this.c.clientHeight;
    if (!w || !h) return;
    this.w = w; this.h = h;
    this.c.width = Math.round(w * dpr); this.c.height = Math.round(h * dpr);
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    var cols = Math.floor(w / PITCH), rows = Math.floor(h / PITCH);
    var ox = (w - (cols - 1) * PITCH) / 2, oy = (h - (rows - 1) * PITCH) / 2;
    var xs = [], ys = [], sh = [];
    for (var c = 0; c < cols; c++) {
      if (c % (SHARD_COLS + 1) === SHARD_COLS) continue; // gap between shards
      for (var r = 0; r < rows; r++) { xs.push(ox + c * PITCH); ys.push(oy + r * PITCH); sh.push(Math.floor(c / (SHARD_COLS + 1))); }
    }
    this.n = xs.length;
    this.rows = rows;
    this.x = Float32Array.from(xs); this.y = Float32Array.from(ys); this.shard = Uint16Array.from(sh);
    this.shards = sh.length ? sh[sh.length - 1] + 1 : 0;
    this.ink = new Float32Array(this.n);
    this.blue = new Float32Array(this.n);
    this.phase = new Float32Array(this.n);
    this.scanned = new Uint8Array(this.n);
    this.hit = new Float32Array(this.n);   // 0 = no hit, otherwise reveal time (ms)
    this.owner = new Int32Array(this.n).fill(-1);
    this.hitDim = new Uint8Array(this.n);
    for (var i = 0; i < this.n; i++) this.phase[i] = rand(i, 7) * Math.PI * 2;
    if (this.opts.onLayout) this.opts.onLayout(this.n, this.shards);
    if (this.mode === 'settled') this.placeHits(this.lastFiles || []);
  };

  // Called on every input change. `caret` is 0..1 along the query.
  Field.prototype.type = function (query, caret) {
    var len = query.trim().length;
    this.seed = hash(query);
    this.frac = len === 0 ? 0 : Math.max(0.012, 0.42 * Math.pow(0.62, len - 1));
    this.energy = Math.min(1, this.energy + 0.22);
    if (this.mode === 'settled' || this.mode === 'empty') this.mode = len ? 'typing' : 'idle';
    else if (this.mode !== 'scan') this.mode = len ? 'typing' : 'idle';
    if (!this.reduced && this.w) {
      var cx = this.w * (0.08 + 0.84 * (caret == null ? rand(len, 3) : caret));
      this.ripples.push({ x: cx, y: this.h * (0.3 + 0.4 * rand(this.seed, 11)), t: performance.now() });
      if (this.ripples.length > 8) this.ripples.shift();
    }
  };

  Field.prototype.candidates = function () { return Math.round(this.frac * this.n); };

  Field.prototype.scan = function () {
    this.labels = []; this.labelBoxes = [];
    this.mode = 'scan';
    this.pending = null;
    this.sweep = 0; this.passes = 0;
    this.speed = 0.5;
    this.scanned.fill(0);
    this.hit.fill(0);
    this.owner.fill(-1);
    this.focusKey = null;
  };

  // files: [{ key, n, label, href }]; n = number of matches, drawn as a small cluster; href makes it clickable.
  // A fast response still gets a complete sweep: the band speeds up and settles once it leaves the right edge.
  Field.prototype.settle = function (files) {
    if (this.mode === 'scan' && !this.reduced && this.n && this.sweep < this.w) { this.pending = files; this.speed = Math.max(this.speed, 4); return; }
    this.pending = null;
    this.lastFiles = files;
    this.mode = files.length ? 'settled' : 'empty';
    this.flashAt = performance.now();
    this.placeHits(files);
  };

  Field.prototype.placeHits = function (files) {
    if (!this.n) return;
    var now = performance.now(), self = this, originX = this.sweep;
    var rows = this.rows, cols = Math.floor(this.n / rows);
    this.hit.fill(0);
    this.hitDim.fill(0);
    this.owner.fill(-1);
    this.files = {};
    this.labels = []; this.labelBoxes = [];
    var stagger = Math.min(30, 300 / Math.max(1, files.length));
    files.forEach(function (f, fi) {
      var hsh = hash(f.key);
      var col = hsh % Math.max(1, cols - 12), row = 1 + (hsh >>> 8) % Math.max(1, rows - 6);
      var k = 16, lit = Math.min(16, 7 + f.n * 3);
      var idxs = [];
      for (var j = 0; j < k; j++) {
        var i = (col + Math.floor(j / 4)) * rows + row + (j % 4);
        if (i >= self.n) continue;
        idxs.push(i);
        self.hit[i] = now + 40 + Math.abs(self.x[i] - originX) * 0.12 + fi * stagger + j * 10;
        self.hitDim[i] = j >= lit ? 1 : 0;
        self.owner[i] = fi;
      }
      self.files[f.key] = idxs;
      if (idxs.length) self.labels.push({ key: f.key, text: f.label || '', i: idxs[idxs.length - 1], at: self.hit[idxs[0]] + 180 });
    });
  };

  Field.prototype.focus = function (key) { this.focusKey = key; };

  Field.prototype.reset = function () {
    this.labels = [];
    this.pending = null; this.labelBoxes = [];
    this.mode = 'idle'; this.frac = 0; this.hit.fill(0); this.owner.fill(-1); this.scanned.fill(0); this.focusKey = null; this.lastFiles = [];
  };

  Field.prototype.frame = function (now) {
    var dt = Math.min(64, now - this.last); this.last = now;
    if (!this.n) return;
    var ctx = this.ctx, n = this.n, w = this.w;
    this.energy = Math.max(0, this.energy - dt * 0.0011);
    var tw = now * (0.0011 + this.energy * 0.006);

    if (this.mode === 'scan') {
      this.speed = Math.min(this.reduced ? 0 : 7, this.speed + dt * 0.0065);
      this.sweep += this.speed * dt;
      if (this.pending && this.sweep > w + 40) this.settle(this.pending);
      else if (this.sweep > w + 80) { this.sweep = -40; this.passes++; this.scanned.fill(0); }
      if (this.opts.onScan) this.opts.onScan(Math.min(1, (this.passes + Math.max(0, this.sweep) / w)));
    }

    var focusSet = null;
    if (this.focusKey && this.files[this.focusKey]) { focusSet = {}; this.files[this.focusKey].forEach(function (i) { focusSet[i] = 1; }); }
    var flash = Math.exp(-(now - this.flashAt) / 220);
    var settled = this.mode === 'settled' || this.mode === 'empty';
    var base = this.dark ? 0.15 : 0.13;
    var ripples = this.ripples.filter(function (r) { return now - r.t < 1100; });
    this.ripples = ripples;

    var inkBins = [], blueBins = [];
    for (var b = 0; b <= LEVELS; b++) { inkBins.push([]); blueBins.push([]); }

    for (var i = 0; i < n; i++) {
      var x = this.x[i], y = this.y[i];
      var twinkle = 0.5 + 0.5 * Math.sin(tw * (1 + (i % 7) * 0.13) + this.phase[i]);
      var ink = base * (0.55 + 0.45 * twinkle);
      var blue = 0;

      if (this.frac > 0 && !settled && rand(i, this.seed) < this.frac) ink += 0.22 + 0.12 * twinkle;

      for (var k = 0; k < ripples.length; k++) {
        var rp = ripples[k], age = now - rp.t, dx = x - rp.x, dy = y - rp.y;
        var d = Math.sqrt(dx * dx + dy * dy) - age * 0.42;
        if (d > -22 && d < 22) ink += 0.42 * (1 - age / 1100) * (1 - Math.abs(d) / 22);
      }

      if (this.mode === 'scan') {
        var ds = this.sweep - x;
        if (ds > -36 && ds < 0) blue = Math.max(blue, 1 + ds / 36);
        else if (ds >= 0 && ds < 260) { blue = Math.max(blue, 0.9 * Math.exp(-ds / 70)); this.scanned[i] = 1; }
        if (this.scanned[i]) ink += 0.1;
      }

      if (settled) {
        ink = base * 0.55 * (0.7 + 0.3 * twinkle) + 0.4 * flash;
        if (this.frac > 0 && rand(i, this.seed) < this.frac) ink += 0.1;
        var h = this.hit[i];
        if (h && now >= h) {
          var pop = Math.min(1, (now - h) / 260);
          ink = (this.hitDim[i] ? 0.12 + 0.2 * pop : 0.3 + 0.7 * pop);
          if (focusSet && focusSet[i]) { blue = 1; ink = 0; }
        }
      }

      if (ink > 1) ink = 1;
      var target = inkBins[Math.round(ink * LEVELS)];
      target.push(i);
      if (blue > 0.02) blueBins[Math.round(Math.min(1, blue) * LEVELS)].push(i);
    }

    ctx.clearRect(0, 0, w, this.h);
    this.drawBins(inkBins, this.inkColor, R);
    this.drawBins(blueBins, this.liveColor, R + 0.25);
    if (settled && this.labels.length) {
      ctx.font = '500 10.5px "JetBrains Mono", ui-monospace, monospace';
      ctx.textBaseline = 'middle';
      var placed = [], maxLabels = w < 600 ? 3 : 6, self = this;
      this.labelBoxes = [];
      Object.keys(this.files).forEach(function (key) {
        var idxs = self.files[key];
        if (!idxs.length) return;
        var a = idxs[0], z = idxs[idxs.length - 1];
        placed.push([self.x[a] - 4, self.y[a] - 4, self.x[z] + 4, self.y[z] + 4]);
      });
      var clashes = function (box) {
        if (box[0] < 2 || box[2] > w - 2) return true;
        for (var q = 0; q < placed.length; q++) { var o = placed[q]; if (box[0] < o[2] && box[2] > o[0] && box[1] < o[3] && box[3] > o[1]) return true; }
        return false;
      };
      for (var l = 0; l < Math.min(maxLabels, this.labels.length); l++) {
        var lb = this.labels[l];
        if (now < lb.at || !lb.text) continue;
        var wText = ctx.measureText(lb.text).width, ly = this.y[lb.i] - PITCH * 1.5;
        var right = this.x[lb.i] + 10, left = this.x[lb.i] - PITCH * 3 - 10 - wText, lx = null;
        [right, left].some(function (cand) {
          if (clashes([cand - 4, ly - 8, cand + wText + 4, ly + 8])) return false;
          lx = cand; return true;
        });
        if (lx === null) continue;
        placed.push([lx - 4, ly - 8, lx + wText + 4, ly + 8]);
        this.labelBoxes.push({ key: lb.key, box: [lx - 4, ly - 8, lx + wText + 4, ly + 8] });
        ctx.globalAlpha = Math.min(1, (now - lb.at) / 240) * 0.85;
        ctx.fillStyle = this.focusKey === lb.key ? this.liveColor : this.inkColor;
        ctx.fillText(lb.text, lx, ly);
      }
    }
    ctx.globalAlpha = 1;
  };

  Field.prototype.drawBins = function (bins, color, r) {
    var ctx = this.ctx;
    ctx.fillStyle = color;
    for (var lv = 1; lv <= LEVELS; lv++) {
      var list = bins[lv];
      if (!list.length) continue;
      ctx.globalAlpha = lv / LEVELS;
      ctx.beginPath();
      for (var j = 0; j < list.length; j++) {
        var i = list[j], rr = r + (this.hit[i] && !this.hitDim[i] && lv > LEVELS * 0.8 ? 0.7 : 0);
        ctx.moveTo(this.x[i] + rr, this.y[i]);
        ctx.arc(this.x[i], this.y[i], rr, 0, Math.PI * 2);
      }
      ctx.fill();
    }
  };

  window.Field = Field;
  window.Field.hash = hash;
})();
