const Utilization = (() => {
    'use strict';

    const $ = (id) => document.getElementById(id);

    const MIN_SPAN = 1000; // smallest zoom: 1 second
    const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

    const cache = new Map(); // asset id -> parsed asset
    let asset = null;        // currently shown asset (null until the first load)
    let view = { from: 0, to: 0 };

    // ---------- formatting ----------

    const pad = (n) => String(n).padStart(2, '0');

    function toInput(ms) {
        const d = new Date(ms);
        return d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()) +
            'T' + pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
    }
    const fmtFull = (ms) => toInput(ms).replace('T', ' ');
    const fmtNum = (n) => n.toLocaleString(undefined, { maximumFractionDigits: 1 });
    const level = (pct) => (pct >= 95 ? 'full' : pct >= 75 ? 'high' : pct >= 40 ? 'mid' : 'low');
    const span = () => view.to - view.from;

    // ---------- loading ----------

    // The page already knows id, bandwidth, start and end (data-* attributes on
    // the button). Only the slots are fetched: POST /slots?id=<id> -> [ {starts_at, stops_at, available}, ... ]
    // All timestamps from the server are unix seconds; internally everything is milliseconds.
    async function load(data) {
        if (cache.has(data.id)) return cache.get(data.id);
        const res = await fetch('/asassets/data?id=' + encodeURIComponent(data.id), { method: 'POST' });
        if (!res.ok) throw new Error('Could not load utilization (' + res.status + ')');
        const rawSlots = await res.json();
        const a = {
            id: data.id,
            bandwidth: Number(data.bandwidth),
            price: Number(data.price),
            start: Number(data.start) * 1000, // unix seconds -> ms for JS Date
            end: Number(data.end) * 1000,
            slots: rawSlots
                .map((s) => ({ s: s.starts_at * 1000, e: s.stops_at * 1000, avail: s.available }))
                .sort((p, q) => p.s - q.s),
        };
        cache.set(data.id, a);
        return a;
    }

    function showError(msg) {
        const err = $('viz-error');
        err.textContent = msg;
        err.hidden = !msg;
    }

    function markActiveRow(id) {
        document.querySelectorAll('tr[data-id]').forEach((tr) => {
            tr.classList.toggle('util-active', tr.dataset.id === id);
        });
    }

    // ---------- public actions (called from inline handlers) ----------

    // onclick on the row button: loads the data, then shows the hidden section.
    async function visualize(btn) {
        const label = btn.textContent;
        btn.disabled = true;
        btn.textContent = 'Loading\u2026';
        try {
            const a = await load(btn.dataset);
            asset = a;
            $('viz-title').textContent = "Asset ID " + a.id;
            $('stat-bw').textContent = fmtNum(a.bandwidth);
            showError('');
            view = { from: a.start, to: a.end };
            $('viz').hidden = false;
            markActiveRow(a.id);
            render();
            $('viz').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
        } catch (err) {
            $('viz').hidden = false;
            showError(err.message);
        } finally {
            btn.disabled = false;
            btn.textContent = label;
        }
    }

    function hide() {
        $('viz').hidden = true;
        markActiveRow(null);
    }

    function setView(from, to) {
        if (!asset) return;
        const sp = Math.max(to - from, MIN_SPAN);
        if (sp >= asset.end - asset.start) {
            view = { from: asset.start, to: asset.end };
        } else {
            if (from < asset.start) from = asset.start;
            if (from + sp > asset.end) from = asset.end - sp;
            view = { from, to: from + sp };
        }
        render();
    }

    function apply() {
        if (!asset) return;
        const f = new Date($('from').value).getTime();
        const t = new Date($('to').value).getTime();
        if (Number.isNaN(f) || Number.isNaN(t) || t <= f) {
            showError('Enter a valid range: "To" must be after "From".');
            return;
        }
        showError('');
        setView(f, t);
    }

    function zoomIn() {
        if (!asset) return;
        const c = (view.from + view.to) / 2;
        const h = span() / 4;
        setView(c - h, c + h);
    }

    function zoomOut() {
        if (!asset) return;
        const c = (view.from + view.to) / 2;
        const h = span();
        setView(c - h, c + h);
    }

    function panLeft() {
        if (asset) setView(view.from - span() / 2, view.to - span() / 2);
    }

    function panRight() {
        if (asset) setView(view.from + span() / 2, view.to + span() / 2);
    }

    function reset() {
        if (asset) setView(asset.start, asset.end);
    }

    // ---------- drag-to-zoom (pointer handlers on the plot) ----------

    let dragX0 = null;
    let brushEl = null;

    function localX(ev) {
        const rect = ev.currentTarget.getBoundingClientRect();
        return { x: Math.min(Math.max(ev.clientX - rect.left, 0), rect.width), width: rect.width };
    }

    function brushStart(ev) {
        if (!asset || ev.button !== 0) return;
        dragX0 = localX(ev).x;
        brushEl = document.createElement('div');
        brushEl.className = 'util-brush';
        brushEl.style.left = dragX0 + 'px';
        brushEl.style.width = '0px';
        ev.currentTarget.appendChild(brushEl);
        ev.currentTarget.setPointerCapture(ev.pointerId);
    }

    function brushMove(ev) {
        if (dragX0 === null) return;
        const { x } = localX(ev);
        brushEl.style.left = Math.min(x, dragX0) + 'px';
        brushEl.style.width = Math.abs(x - dragX0) + 'px';
    }

    function brushEnd(ev) {
        if (dragX0 === null) return;
        const { x, width } = localX(ev);
        const x0 = dragX0;
        brushCancel();
        if (Math.abs(x - x0) > 5) {
            const toTime = (px) => view.from + (px / width) * span();
            setView(toTime(Math.min(x, x0)), toTime(Math.max(x, x0)));
        }
    }

    function brushCancel() {
        dragX0 = null;
        if (brushEl) {
            brushEl.remove();
            brushEl = null;
        }
    }

    // ---------- axis ticks ----------

    const S = 1000, M = 60 * S, H = 60 * M, D = 24 * H;
    const STEPS = [S, 2 * S, 5 * S, 10 * S, 15 * S, 30 * S, M, 2 * M, 5 * M, 10 * M, 15 * M, 30 * M,
        H, 2 * H, 3 * H, 6 * H, 12 * H, D, 2 * D, 7 * D, 14 * D, 30 * D, 90 * D, 365 * D];

    function makeTicks(from, to) {
        const sp = to - from;
        const step = STEPS.find((s) => sp / s <= 8) || STEPS[STEPS.length - 1];
        const off = -new Date(from).getTimezoneOffset() * M; // align ticks to local time
        const ticks = [];
        for (let t = Math.ceil((from + off) / step) * step - off; t <= to; t += step) ticks.push(t);

        const label = (t) => {
            const d = new Date(t);
            const date = d.getDate() + ' ' + MONTHS[d.getMonth()];
            const hm = pad(d.getHours()) + ':' + pad(d.getMinutes());
            if (step >= D) return date;
            const time = step < M ? hm + ':' + pad(d.getSeconds()) : hm;
            return sp >= D ? date + ' ' + time : time;
        };
        return ticks.map((t) => ({ t, text: label(t) }));
    }

    // ---------- rendering ----------

    function render() {
        const { from, to } = view;
        const sp = to - from;
        const bw = asset.bandwidth;
        const pos = (t) => ((t - from) / sp) * 100;

        const plot = $('plot');
        const xAxis = $('x');
        const yAxis = $('y');

        $('from').value = toInput(from);
        $('to').value = toInput(to);
        $('from').min = $('to').min = toInput(asset.start);
        $('from').max = $('to').max = toInput(asset.end);

        plot.replaceChildren();
        xAxis.replaceChildren();
        yAxis.replaceChildren();

        // y axis + horizontal grid lines (0 .. bandwidth)
        [0, 25, 50, 75, 100].forEach((p) => {
            const lbl = document.createElement('span');
            lbl.textContent = fmtNum((bw * p) / 100);
            lbl.style.bottom = p + '%';
            if (p === 100) lbl.classList.add('util-top');
            yAxis.appendChild(lbl);
            if (p > 0) {
                const line = document.createElement('div');
                line.className = 'util-hline';
                line.style.bottom = p + '%';
                plot.appendChild(line);
            }
        });

        // x axis + vertical grid lines
        makeTicks(from, to).forEach(({ t, text }) => {
            const lbl = document.createElement('span');
            lbl.textContent = text;
            lbl.style.left = pos(t) + '%';
            xAxis.appendChild(lbl);
            const line = document.createElement('div');
            line.className = 'util-vline';
            line.style.left = pos(t) + '%';
            plot.appendChild(line);
        });

        // bars: only slots intersecting the view, clipped to it
        const frag = document.createDocumentFragment();
        const addGap = (a, b) => {
            const gap = document.createElement('div');
            gap.className = 'util-gap';
            gap.style.left = pos(a) + '%';
            gap.style.width = (pos(b) - pos(a)) + '%';
            gap.title = 'No data\n' + fmtFull(a) + ' \u2013 ' + fmtFull(b);
            frag.appendChild(gap);
        };
        let cursor = from; // end of the data seen so far; anything after it is a gap
        let weighted = 0, covered = 0, peak = 0, n = 0, revenue = 0;
        for (const s of asset.slots) {
            if (s.e <= from) continue;
            if (s.s >= to) break; // slots are sorted by start
            const a = Math.max(s.s, from);
            const b = Math.min(s.e, to);
            if (a > cursor) addGap(cursor, a);
            cursor = Math.max(cursor, b);
            const sold = Math.min(Math.max(bw - s.avail, 0), bw);
            const pct = (sold / bw) * 100;
            weighted += pct * (b - a);
            covered += b - a;
            revenue += sold * asset.price * ((b - a) / 1000)
            peak = Math.max(peak, pct);
            n++;

            const seg = document.createElement('div');
            seg.className = 'util-seg util-' + level(pct);
            seg.style.left = pos(a) + '%';
            seg.style.width = (pos(b) - pos(a)) + '%';
            seg.style.setProperty('--used', pct.toFixed(2));
            seg.title = fmtFull(s.s) + ' \u2013 ' + fmtFull(s.e) +
                '\nSold: ' + fmtNum(sold) + ' / ' + fmtNum(bw) + ' (' + pct.toFixed(0) + '%)' +
                '\nAvailable: ' + fmtNum(s.avail);
            frag.appendChild(seg);
        }
        if (cursor < to) addGap(cursor, to);
        // bars go first in the DOM so the grid lines draw on top of them
        plot.insertBefore(frag, plot.firstChild);

        $('stat-avg').textContent = covered ? (weighted / covered).toFixed(0) + '%' : '\u2013';
        $('stat-peak').textContent = n ? peak.toFixed(0) + '%' : '\u2013';
        $('stat-revenue').textContent = n ? fmtNum(Math.floor(revenue)) : '\u2013';
        $('stat-count').textContent = String(n);
    }

    return {
        visualize, hide, apply, zoomIn, zoomOut, panLeft, panRight, reset,
        brushStart, brushMove, brushEnd, brushCancel
    };
})();
