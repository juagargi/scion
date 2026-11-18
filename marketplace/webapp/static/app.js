/*
 * Marketplace web UI. One script for all pages, loaded with "defer".
 *
 *   1. Common:        navigation highlighting (all pages)
 *   2. Accounts:      account, asset and reservation actions (account / assets / reservations pages)
 *   3. Utilization:   asset utilization chart (AS assets page)
 *   4. Setup:         topology based asset setup (AS setup page)
 *   5. Table filters: ingress / egress / time / bandwidth filters (assets, reservations, AS assets pages)
 *
 * Functions in sections 2 and 3 are global because the templates call them from inline handlers.
 * Sections 4 and 5 only initialize themselves when their elements are on the page.
 */

// ===================================================================== 1. Common

(function () {
    'use strict';

    // Marks the navigation link of the current page.
    document.addEventListener('DOMContentLoaded', () => {
        const path = window.location.pathname.replace(/\/+$/, '') || '/';
        document.querySelectorAll('nav a[href]').forEach((a) => {
            if (a.getAttribute('href') === path) a.setAttribute('aria-current', 'page');
        });
    });
})();

// ===================================================================== 2. Accounts

var editingAccountId;
var mainAccountId;

function editBalance(id, name) {
    editingAccountId = id;
    mainAccountId = document.getElementById("main-account-id").textContent;
    document.getElementById("balance-editor").style.display = "block";
    document.getElementById("balance-user-name").textContent = name;
    document.getElementById("balance-value").value = 0;
}
async function saveBalance() {
    const balance = document.getElementById("balance-value").value;

    const response = await fetch(`/account/balance`, {
        method: "POST",
        headers: {
            "Content-Type": "application/x-www-form-urlencoded"
        },
        body: "from=" + encodeURIComponent(mainAccountId) + "&to=" + encodeURIComponent(editingAccountId) + "&amount=" + encodeURIComponent(balance)
    });

    if (!response.ok) {
        if (response.status == 401) {
            location.reload();
            return;
        }
        document.getElementById("error").textContent = await response.text();
        return;
    }

    location.reload();
}
async function requestJWT(id, name) {
    const response = await fetch("/account/token", {
        method: "POST",
        headers: {
            "Content-Type": "application/x-www-form-urlencoded"
        },
        body: "id=" + encodeURIComponent(id)
    });

    if (!response.ok) {
        if (response.status == 401) {
            location.reload();
            return;
        }
        document.getElementById("error").textContent = await response.text()
        return;
    }

    const token = await response.text();

    document.getElementById("jwt-token").innerText = token;
    document.getElementById("jwt-container").style.display = "block";
    document.getElementById("jwt-user-name").textContent = name;
}
async function deleteAccount(id, name) {
    if (!confirm(`Delete account "${name}"?`)) {
        return;
    }

    const response = await fetch("/account/delete", {
        method: "POST",
        headers: {
            "Content-Type": "application/x-www-form-urlencoded"
        },
        body: "id=" + encodeURIComponent(id)
    });

    if (!response.ok) {
        if (response.status == 401) {
            location.reload();
            return;
        }
        document.getElementById("error").textContent = await response.text();
        return;
    }

    location.reload();
}
async function resetJWT(id, name) {
    if (!confirm(`Reset JWT token for account "${name}"?`)) {
        return;
    }

    const response = await fetch("/account/resetjwt", {
        method: "POST",
        headers: {
            "Content-Type": "application/x-www-form-urlencoded"
        },
        body: "id=" + encodeURIComponent(id)
    });

    if (!response.ok) {
        if (response.status == 401) {
            location.reload();
            return;
        }
        document.getElementById("error").textContent = await response.text();
        return;
    }
    location.reload();
}
function copyJWT() {
    const token = document.getElementById("jwt-token").innerText;

    navigator.clipboard.writeText(token)
        .then(() => {
            const element = document.getElementById("jwt-token");
            const oldText = element.dataset.originalText || token;

            element.dataset.originalText = oldText;
            element.innerText = "Copied!";

            setTimeout(() => {
                element.innerText = oldText;
            }, 500);
        })
        .catch(err => {
            document.getElementById("error").textContent = "Could not copy token: " + err;
        });
}

async function assignAsset(id, accountid, select) {
    const response = await fetch("/assets/assign", {
        method: "POST",
        headers: {
            "Content-Type": "application/x-www-form-urlencoded"
        },
        body: "id=" + encodeURIComponent(id) + "&from=" + encodeURIComponent(accountid) + "&to=" + encodeURIComponent(select.value)
    });

    if (!response.ok) {
        if (response.status == 401) {
            location.reload();
            return;
        }
        document.getElementById("error").textContent = await response.text();
        return;
    }
    location.reload();
}
async function assignReservation(id, accountid, select) {
    const response = await fetch("/reservations/assign", {
        method: "POST",
        headers: {
            "Content-Type": "application/x-www-form-urlencoded"
        },
        body: "id=" + encodeURIComponent(id) + "&from=" + encodeURIComponent(accountid) + "&to=" + encodeURIComponent(select.value)
    });

    if (!response.ok) {
        if (response.status == 401) {
            location.reload();
            return;
        }
        document.getElementById("error").textContent = await response.text();
        return;
    }
    location.reload();
}
function reloadWithId(id) {
    const url = new URL(window.location.href);
    url.searchParams.set("id", id);
    window.location.href = url.toString();
}

// ===================================================================== 3. Utilization

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

    // All dates and times are shown and entered in UTC.
    function toInput(ms) {
        const d = new Date(ms);
        return d.getUTCFullYear() + '-' + pad(d.getUTCMonth() + 1) + '-' + pad(d.getUTCDate()) +
            'T' + pad(d.getUTCHours()) + ':' + pad(d.getUTCMinutes()) + ':' + pad(d.getUTCSeconds());
    }
    // datetime-local value ("YYYY-MM-DDTHH:MM[:SS]"), read as UTC -> ms (NaN if invalid)
    const fromInput = (raw) => (raw === '' ? NaN : Date.parse(raw + 'Z'));
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
        const f = fromInput($('from').value);
        const t = fromInput($('to').value);
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
        const ticks = []; // aligned to UTC
        for (let t = Math.ceil(from / step) * step; t <= to; t += step) ticks.push(t);

        const label = (t) => {
            const d = new Date(t);
            const date = d.getUTCDate() + ' ' + MONTHS[d.getUTCMonth()];
            const hm = pad(d.getUTCHours()) + ':' + pad(d.getUTCMinutes());
            if (step >= D) return date;
            const time = step < M ? hm + ':' + pad(d.getUTCSeconds()) : hm;
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


// ===================================================================== 4. Setup

(function () {
    'use strict';

    const SVG_NS = 'http://www.w3.org/2000/svg';
    const LOCAL = 0; // interface "0" stands for the local AS itself
    const MAX_U32 = 4294967295; // the server uses uint32 for all numbers in the request

    // Layout constants for the SVG (in viewBox units)
    const L = {
        width: 840, pad: 20, title: 30,
        brX: 50, brW: 290, brHead: 42, row: 50, brGap: 18,
        nbX: 620, nbW: 190, nbH: 40, ifR: 14,
    };

    // Units, largest first. Values are always stored as kbit/s and seconds.
    const BW_UNITS = [[1e6, 'Gbit/s'], [1e3, 'Mbit/s'], [1, 'kbit/s']];
    const TIME_UNITS = [[3600, 'h'], [60, 'min'], [1, 's']];

    const DEFAULT_FIELDS = [
        { key: 'bandwidth_min', id: 'd-bandwidth-min', units: BW_UNITS },
        { key: 'bandwidth_max', id: 'd-bandwidth-max', units: BW_UNITS },
        { key: 'time_granularity', id: 'd-time-granularity', units: TIME_UNITS },
        { key: 'time_min_duration', id: 'd-time-min-duration', units: TIME_UNITS },
        { key: 'time_max_duration', id: 'd-time-max-duration', units: TIME_UNITS },
    ];

    const MODE_TEXT = { pairs: 'per interface pair', interfaces: 'per interface' };

    const state = {
        isdAs: '',
        routers: [],          // [{name, internalAddr, interfaces: [ifc]}]
        interfaces: [],       // all interfaces, sorted by id
        byId: new Map(),      // id -> ifc
        mode: 'pairs',        // 'pairs' | 'interfaces'
        validFrom: null,      // Date
        validUntil: null,     // Date
        defaults: null,       // {bandwidth_min: kbps, ..., time_granularity: s, ...}
        bandwidths: new Map(), // key -> {ingress, egress, kbps} or {interface, direction, kbps}
        prefillNote: '',      // shown in the summary after pre-filling from static info
    };

    let prefilledFor = ''; // static info / topology / mode the table was last pre-filled from
    let el = {};

    document.addEventListener('DOMContentLoaded', init);

    function init() {
        const $ = (id) => document.getElementById(id);
        el = {
            error: $('error'),
            dialog: $('file-dialog'), topoForm: $('topo-form'), topoFile: $('topo-file'), infoFile: $('info-file'),
            infoRatio: $('info-ratio'),
            validFromDate: $('valid-from-date'), validFromTime: $('valid-from-time'),
            validUntilDate: $('valid-until-date'), validUntilTime: $('valid-until-time'),
            formError: $('form-error'),
            view: $('topology-view'), svgHost: $('topology-svg'), topoName: $('topo-name'), editSetup: $('edit-setup'),
            bwSection: $('bandwidth-config'), bwSummary: $('bw-summary'), bwForm: $('bw-form'),
            bwA: $('bw-a'), bwALabel: $('bw-a-label'), bwBWrap: $('bw-b-wrap'), bwB: $('bw-b'),
            bwValue: $('bw-value'), bwUnit: $('bw-unit'), bwAbLabel: $('bw-ab-label'),
            bwBaWrap: $('bw-ba-wrap'), bwBaLabel: $('bw-ba-label'),
            bwDirWrap: $('bw-dir-wrap'), bwDir: $('bw-dir'),
            bwValueRev: $('bw-value-rev'), bwUnitRev: $('bw-unit-rev'),
            bwPrice: $('bw-price'), bwPriceLabel: $('bw-price-label'),
            bwPriceRev: $('bw-price-rev'), bwPriceRevLabel: $('bw-price-rev-label'),
            bwSubmit: $('bw-submit'), bwError: $('bw-error'),
            bwMissing: $('bw-missing'), bwMissingList: $('bw-missing-list'),
            bwTable: $('bw-table'), bwTableHead: document.querySelector('#bw-table thead'),
            bwTableBody: document.querySelector('#bw-table tbody'),
            bwEmpty: $('bw-empty'), create: $('bw-create'), skipOverlap: $('skip-overlap'), submitStatus: $('submit-status'),
            defaults: {},
        };
        if (!el.topoForm) return; // not on the setup page

        for (const f of DEFAULT_FIELDS) {
            el.defaults[f.key] = { input: $(f.id), unit: $(f.id + '-unit') };
        }
        document.querySelectorAll('select.unit-bw').forEach((s) => fillUnits(s, BW_UNITS, 1));
        document.querySelectorAll('select.unit-time').forEach((s) => fillUnits(s, TIME_UNITS, 1));

        el.topoForm.addEventListener('submit', onContinue);
        // The ratio only matters with a static info file
        const syncRatio = () => { el.infoRatio.disabled = el.infoFile.files.length === 0; };
        el.infoFile.addEventListener('change', syncRatio);
        syncRatio();
        el.topoForm.addEventListener('input', (e) => e.target.removeAttribute('aria-invalid'));
        el.editSetup.addEventListener('click', showDialog);
        el.bwForm.addEventListener('submit', onAddBandwidth);
        el.bwA.addEventListener('change', onPairChanged);
        el.bwB.addEventListener('change', onPairChanged);
        el.bwDir.addEventListener('change', onPairChanged);
        el.create.addEventListener('click', onCreate);
        // The arrow follows the bandwidth field being edited (see onPairChanged)
        for (const n of [el.bwValue, el.bwUnit, el.bwPrice, el.bwValueRev, el.bwUnitRev, el.bwPriceRev]) {
            n.addEventListener('focus', onPairChanged);
            n.addEventListener('blur', onPairChanged);
        }
    }

    function fillUnits(select, units, selected) {
        select.replaceChildren(...[...units].reverse().map(([f, name]) => new Option(name, f)));
        select.value = String(selected);
    }

    // ------------------------------------------------------- step 1: dialog

    async function onContinue(ev) {
        ev.preventDefault();
        formError('');
        clearError();

        const topoFile = el.topoFile.files[0];
        if (!topoFile) return formError('Please choose a topology file.', el.topoFile);
        const infoFile = el.infoFile.files[0] || null;

        let settings, topo, info = null, ratio = 0;
        try {
            settings = readSettings();
            if (infoFile) ratio = readRatio();
        } catch (err) {
            return formError(err.message, err.field);
        }
        try {
            topo = parseTopology(await readJson(topoFile));
        } catch (err) {
            return formError(err.message, el.topoFile);
        }
        if (infoFile) {
            try {
                info = parseStaticInfo(await readJson(infoFile), infoFile.name);
            } catch (err) {
                return formError(err.message, el.infoFile);
            }
        }

        // Keep what was entered before, as long as it fits the mode and topology.
        const prefix = settings.mode === 'pairs' ? 'p:' : 'i:';
        const entries = new Map([...state.bandwidths].filter(([k, e]) =>
            k.startsWith(prefix) && entryIds(e).every((id) => id === LOCAL || topo.byId.has(id))));

        // Pre-fill from the static info once per info file / topology / mode / ratio, and only where
        // nothing is set yet, so manual edits and deletions survive going back and continuing again.
        // When any of these change, entries from the previous pre-fill that were not edited since
        // are replaced (or removed, without a file). A ratio of 0% adds the entries with 0 kbit/s.
        const id = info ? [fileId(infoFile), fileId(topoFile), settings.mode, ratio].join('|') : '';
        if (id !== prefilledFor) {
            for (const [k, e] of entries) if (e.prefilled) entries.delete(k);
            state.prefillNote = '';
            if (info) {
                const added = prefill(entries, info, settings.mode, topo.byId, ratio);
                const n = `${added} ${added === 1 ? 'entry' : 'entries'}`;
                state.prefillNote = ratio > 0
                    ? `Pre-filled ${n} at ${ratio}% of the capacities in ${infoFile.name}.`
                    : `Added ${n} from ${infoFile.name} with 0 kbit/s (ratio 0%); entries left at 0 kbit/s are not submitted.`;
            }
            prefilledFor = id;
        }

        Object.assign(state, topo, settings);
        state.bandwidths = entries;

        renderTopology();
        renderSummary();
        setupBandwidthForm();
        renderBandwidths();
        showView();
    }

    function fileId(f) {
        return `${f.name}:${f.size}:${f.lastModified}`;
    }

    // Percentage of the static capacities to pre-fill; 0 (or empty) = not specified (entries get 0 kbit/s).
    function readRatio() {
        const raw = el.infoRatio.value.trim();
        if (raw === '') return 0;
        const v = Number(raw);
        if (!Number.isFinite(v) || v < 0 || v > 100) {
            throw fieldError('Bandwidth ratio must be between 0% and 100%.', el.infoRatio);
        }
        return v;
    }

    function fieldError(message, field) {
        const err = new Error(message);
        err.field = field;
        return err;
    }

    function readSettings() {
        const mode = el.topoForm.querySelector('input[name="bw-mode"]:checked')?.value || 'pairs';

        const validFrom = readDateTime(el.validFromDate, el.validFromTime, 'start');
        const validUntil = readDateTime(el.validUntilDate, el.validUntilTime, 'end');
        if (validUntil <= validFrom) {
            throw fieldError('The end must be after the start.', el.validUntilTime.value ? el.validUntilTime : el.validUntilDate);
        }

        const defaults = {};
        for (const f of DEFAULT_FIELDS) {
            const { input, unit } = el.defaults[f.key];
            const raw = input.value.trim();
            const v = Number(raw);
            if (raw === '' || !Number.isFinite(v) || v < 0) {
                throw fieldError(`${f.key}: please enter a number of at least 0.`, input);
            }
            defaults[f.key] = Math.round(v * Number(unit.value));
            if (defaults[f.key] > MAX_U32) {
                throw fieldError(`${f.key}: the value is too large (at most ${MAX_U32} ${f.units === BW_UNITS ? 'kbit/s' : 's'}).`, input);
            }
        }

        const d = defaults;
        const inp = (k) => el.defaults[k].input;
        if (d.bandwidth_max <= 0) throw fieldError('bandwidth_max must be greater than 0.', inp('bandwidth_max'));
        if (d.bandwidth_min > d.bandwidth_max) {
            throw fieldError('bandwidth_min must not be greater than bandwidth_max.', inp('bandwidth_min'));
        }
        if (d.time_granularity < 1) {
            throw fieldError('time_granularity must be at least 1 second.', inp('time_granularity'));
        }
        for (const k of ['time_min_duration', 'time_max_duration']) {
            if (d[k] <= 0 || d[k] % d.time_granularity !== 0) {
                throw fieldError(`${k} must be a positive multiple of time_granularity ` +
                    `(${formatDuration(d.time_granularity)}).`, inp(k));
            }
        }
        if (d.time_min_duration > d.time_max_duration) {
            throw fieldError('time_min_duration must not be greater than time_max_duration.', inp('time_min_duration'));
        }

        return { mode, validFrom, validUntil, defaults };
    }

    // Shows a value in the largest unit that divides it evenly.
    function setWithUnit(input, select, value, units) {
        let factor = 1;
        for (const [f] of units) {
            if (value !== 0 && value % f === 0) { factor = f; break; }
        }
        select.value = String(factor);
        input.value = String(value / factor);
    }

    // Date ("YYYY-MM-DD", required) plus time ("HH:MM[:SS]", optional, default 00:00:00), in UTC.
    function readDateTime(dateInput, timeInput, what) {
        const dm = /^(\d{4})-(\d{2})-(\d{2})$/.exec(dateInput.value);
        if (!dm) throw fieldError(`Please enter a valid ${what} date.`, dateInput);
        let h = 0, min = 0, s = 0;
        if (timeInput.value) {
            const tm = /^(\d{2}):(\d{2})(?::(\d{2})(?:\.\d+)?)?$/.exec(timeInput.value);
            if (!tm) throw fieldError(`Please enter a valid ${what} time, or leave it empty for 00:00:00.`, timeInput);
            [h, min, s] = [+tm[1], +tm[2], +(tm[3] || 0)];
        }
        const d = new Date(Date.UTC(+dm[1], +dm[2] - 1, +dm[3], h, min, s));
        if (isNaN(d)) throw fieldError(`Please enter a valid ${what} date.`, dateInput);
        return d;
    }


    // ---------------------------------------------------------------- files

    async function readJson(file) {
        const text = await file.text();
        try {
            return JSON.parse(text);
        } catch (e) {
            throw new Error(`${file.name}: invalid JSON (${e.message})`);
        }
    }

    function isObject(v) {
        return v !== null && typeof v === 'object' && !Array.isArray(v);
    }

    function toIfId(v) {
        if (typeof v !== 'number' && typeof v !== 'string') return null;
        const n = Number(v);
        return v !== '' && Number.isSafeInteger(n) && n >= 0 ? n : null;
    }

    function parseTopology(obj) {
        if (!isObject(obj) || !isObject(obj.border_routers)) {
            throw new Error('Topology file: missing "border_routers" object.');
        }
        const routers = [];
        const interfaces = [];
        const byId = new Map();

        for (const [brName, br] of Object.entries(obj.border_routers)) {
            if (!isObject(br) || !isObject(br.interfaces) || Object.keys(br.interfaces).length === 0) {
                throw new Error(`Border router "${brName}" has no interfaces.`);
            }
            const router = { name: brName, internalAddr: String(br.internal_addr || ''), interfaces: [] };

            for (const [idStr, ifc] of Object.entries(br.interfaces)) {
                const id = Number(idStr);
                if (!/^\d+$/.test(idStr) || !Number.isSafeInteger(id)) {
                    throw new Error(`Border router "${brName}": invalid interface id "${idStr}".`);
                }
                if (id === LOCAL) {
                    throw new Error(`Border router "${brName}": interface id 0 is reserved for "local".`);
                }
                if (byId.has(id)) {
                    throw new Error(`Interface ${id} is defined on both "${byId.get(id).router}" and "${brName}".`);
                }
                const i = isObject(ifc) ? ifc : {};
                const underlay = isObject(i.underlay) ? i.underlay : {};
                const entry = {
                    id,
                    router: brName,
                    isdAs: String(i.isd_as ?? '?'),
                    linkTo: String(i.link_to ?? ''),
                    mtu: i.mtu,
                    local: String(underlay.local || ''),
                    remote: String(underlay.remote || ''),
                };
                byId.set(id, entry);
                router.interfaces.push(entry);
                interfaces.push(entry);
            }
            router.interfaces.sort((a, b) => a.id - b.id);
            routers.push(router);
        }
        if (routers.length === 0) throw new Error('Topology file: "border_routers" is empty.');

        interfaces.sort((a, b) => a.id - b.id);
        return {
            isdAs: typeof obj.isd_as === 'string' ? obj.isd_as : '',
            routers, interfaces, byId,
        };
    }

    // Static info: { "Bandwidth": { "<if>": { "Inter": capacity, "Intra": { "<other if>": capacity } } } }
    // Capacities are in kbit/s. Anything missing or unusable is skipped.
    // Intra of interface x towards y is taken as the capacity for x → y.
    function parseStaticInfo(obj, name) {
        if (!isObject(obj) || !isObject(obj.Bandwidth)) throw new Error(`${name}: missing "Bandwidth" object.`);
        const usable = (v) => typeof v === 'number' && Number.isFinite(v) && v > 0;
        const inter = new Map(); // id -> capacity
        const intra = [];        // [{ingress, egress, capacity}]
        for (const [idStr, entry] of Object.entries(obj.Bandwidth)) {
            const id = toIfId(idStr);
            if (id === null || id === LOCAL || !isObject(entry)) continue;
            if (usable(entry.Inter)) inter.set(id, entry.Inter);
            if (!isObject(entry.Intra)) continue;
            for (const [otherStr, capacity] of Object.entries(entry.Intra)) {
                const other = toIfId(otherStr);
                if (other === null || other === LOCAL || other === id || !usable(capacity)) continue;
                intra.push({ ingress: id, egress: other, capacity });
            }
        }
        return { inter, intra };
    }

    // Adds entries at ratio% of the capacity where none exist yet; returns how many were added.
    // Inter → 0 ↔ x (pairs) or ingress/egress of x (per interface); Intra → x → y (pairs only).
    // Added entries are marked "prefilled" until the user saves them through the form.
    function prefill(entries, info, mode, byId, ratio) {
        // Rounded down so the value never exceeds the share of the capacity; at least 1 kbit/s
        // 0% gives 0 kbit/s, which means "not specified yet"
        const share = (capacity) => ratio === 0
            ? 0
            : Math.min(MAX_U32, Math.max(1, Math.floor(capacity * ratio / 100 + 1e-9)));
        let added = 0;
        const add = (e) => {
            const k = keyOf(e);
            if (!entries.has(k)) {
                entries.set(k, { ...e, prefilled: true });
                added++;
            }
        };
        for (const [id, capacity] of info.inter) {
            if (!byId.has(id)) continue;
            const kbps = share(capacity);
            if (mode === 'pairs') {
                add({ ingress: LOCAL, egress: id, kbps, price: null });
                add({ ingress: id, egress: LOCAL, kbps, price: null });
            } else {
                add({ interface: id, direction: 'ingress', kbps, price: null });
                add({ interface: id, direction: 'egress', kbps, price: null });
            }
        }
        if (mode === 'pairs') {
            for (const e of info.intra) {
                if (byId.has(e.ingress) && byId.has(e.egress)) {
                    add({ ingress: e.ingress, egress: e.egress, kbps: share(e.capacity), price: null });
                }
            }
        }
        return added;
    }

    // ---------------------------------------------------------- visualization

    function svgEl(tag, attrs, text) {
        const n = document.createElementNS(SVG_NS, tag);
        for (const [k, v] of Object.entries(attrs || {})) n.setAttribute(k, v);
        if (text !== undefined) n.textContent = text; // textContent: file contents are never parsed as markup
        return n;
    }

    // Node positions of the current diagram, used to draw the connection between a selected pair
    const diagram = { x: 0, y: new Map(), pairLayer: null };

    function renderTopology() {
        const top = L.pad + L.title;
        const ifX = L.brX + L.brW;
        const ifY = new Map(); // id -> y (including LOCAL)

        const gFrame = svgEl('g');
        const gLinks = svgEl('g');
        const gPair = svgEl('g');   // connection between the selected interfaces
        const gRouters = svgEl('g');
        const gNeighbors = svgEl('g');

        const ifNode = (id, cy, title) => {
            const node = svgEl('g', { class: 'if-node', 'data-if': id });
            node.appendChild(svgEl('title', {}, title));
            node.appendChild(svgEl('circle', { cx: ifX, cy, r: L.ifR }));
            node.appendChild(svgEl('text', { x: ifX, y: cy + 4, 'text-anchor': 'middle' }, String(id)));
            return node;
        };

        // Border routers with their interfaces
        let y = top + 10;
        for (const r of state.routers) {
            const h = L.brHead + r.interfaces.length * L.row;
            const g = svgEl('g', { class: 'br' });
            g.appendChild(svgEl('rect', { x: L.brX, y, width: L.brW, height: h, rx: 8, class: 'br-box' }));
            g.appendChild(svgEl('text', { x: L.brX + 14, y: y + 20, class: 'br-name' }, r.name));
            if (r.internalAddr) {
                g.appendChild(svgEl('text', { x: L.brX + 14, y: y + 35, class: 'small' }, `internal ${r.internalAddr}`));
            }

            r.interfaces.forEach((ifc, i) => {
                const rowTop = y + L.brHead + i * L.row;
                const cy = rowTop + L.row / 2;
                ifY.set(ifc.id, cy);
                g.appendChild(svgEl('line', { x1: L.brX, y1: rowTop, x2: L.brX + L.brW, y2: rowTop, class: 'sep' }));
                g.appendChild(svgEl('text', { x: L.brX + 14, y: cy - 3, class: 'if-label' },
                    `to ${ifc.isdAs}${ifc.linkTo ? ` (${ifc.linkTo})` : ''}`));
                if (ifc.local || ifc.remote) {
                    g.appendChild(svgEl('text', { x: L.brX + 14, y: cy + 13, class: 'small' },
                        `${ifc.local} → ${ifc.remote}`));
                }
                g.appendChild(ifNode(ifc.id, cy, `Interface ${ifc.id}`));
            });
            gRouters.appendChild(g);
            y += h + L.brGap;
        }
        const bottom = y - L.brGap;

        // "0 (local)" node below the border routers, in the same column as the interfaces
        const localY = bottom + L.brGap + L.ifR + 4;
        ifY.set(LOCAL, localY);
        gRouters.appendChild(svgEl('text', { x: L.brX + 14, y: localY + 4, class: 'if-label' }, 'local (this AS)'));
        gRouters.appendChild(ifNode(LOCAL, localY, 'Local (0)'));
        const frameBottom = localY + L.ifR + 10;

        // Frame around the local AS
        gFrame.appendChild(svgEl('rect', {
            x: L.brX - 22, y: top - 8, width: L.brW + 22 + 8, height: frameBottom - top + 8, rx: 12, class: 'as-frame',
        }));
        gFrame.appendChild(svgEl('text', { x: L.brX - 22, y: top - 16, class: 'as-name' },
            state.isdAs ? `AS ${state.isdAs}` : 'Local AS'));

        // Neighbor ASes, in order of first appearance (keeps crossings low)
        const neighbors = [];
        const seen = new Map();
        for (const r of state.routers) {
            for (const ifc of r.interfaces) {
                if (!seen.has(ifc.isdAs)) { seen.set(ifc.isdAs, neighbors.length); neighbors.push(ifc.isdAs); }
            }
        }
        const slot = (bottom - top) / neighbors.length;
        const nbY = neighbors.map((_, k) => top + slot * (k + 0.5)); // center y
        gNeighbors.appendChild(svgEl('text', { x: L.nbX, y: top - 16, class: 'as-name' }, 'Neighbors'));
        neighbors.forEach((name, k) => {
            const g = svgEl('g', { class: 'nb' });
            g.appendChild(svgEl('rect', { x: L.nbX, y: nbY[k] - L.nbH / 2, width: L.nbW, height: L.nbH, rx: 8 }));
            g.appendChild(svgEl('text', { x: L.nbX + L.nbW / 2, y: nbY[k] + 5, 'text-anchor': 'middle' }, name));
            gNeighbors.appendChild(g);
        });

        // Links to the neighbors
        for (const ifc of state.interfaces) {
            const y1 = ifY.get(ifc.id);
            const y2 = nbY[seen.get(ifc.isdAs)];
            const x1 = ifX + L.ifR, x2 = L.nbX;
            const dx = (x2 - x1) / 2;
            const cls = 'link link-' + ifc.linkTo.toLowerCase().replace(/[^a-z0-9_-]/g, '');
            gLinks.appendChild(svgEl('path', {
                d: `M${x1},${y1} C${x1 + dx},${y1} ${x2 - dx},${y2} ${x2},${y2}`,
                class: cls, 'data-if': ifc.id,
            }));
            const label = [ifc.linkTo, ifc.mtu !== undefined ? `MTU ${ifc.mtu}` : ''].filter(Boolean).join(' · ');
            if (label) {
                gLinks.appendChild(svgEl('text', {
                    x: (x1 + x2) / 2, y: (y1 + y2) / 2 - 6, 'text-anchor': 'middle', class: 'link-label',
                }, label));
            }
        }

        const height = frameBottom + L.pad;
        const svg = svgEl('svg', {
            viewBox: `0 0 ${L.width} ${height}`, class: 'topo', role: 'img',
            'aria-label': 'Border routers, interfaces and neighboring ASes',
        });
        // Arrow head for the traffic direction; auto-start-reverse lets the same marker point back
        // to the start of a path (used for ingress: neighbor → interface)
        const defs = svgEl('defs');
        const marker = svgEl('marker', {
            id: 'topo-arrow', viewBox: '0 0 10 10', refX: 9, refY: 5,
            markerWidth: 12, markerHeight: 12, markerUnits: 'userSpaceOnUse', orient: 'auto-start-reverse',
        });
        marker.appendChild(svgEl('path', { d: 'M0,0 L10,5 L0,10 z', class: 'arrow-head' }));
        defs.appendChild(marker);
        svg.append(defs, gFrame, gLinks, gPair, gRouters, gNeighbors);

        diagram.x = ifX;
        diagram.y = ifY;
        diagram.pairLayer = gPair;
        el.svgHost.replaceChildren(svg);
        el.topoName.textContent = state.isdAs ? `(${state.isdAs})` : '';
    }

    // Colors the given interfaces and shows a single arrow for the traffic direction.
    //   Pair [ingress, egress]: connection between both, arrow at the egress end, i.e. on the
    //     egress interface's link pointing to its neighbor (or into the local node for egress 0).
    //   Single interface [x] with dir 'egress': arrow pointing to the neighbor;
    //     'ingress': arrow pointing from the neighbor to the interface.
    function highlight(ids, dir = 'egress') {
        el.svgHost.querySelectorAll('.hl').forEach((n) => n.classList.remove('hl'));
        el.svgHost.querySelectorAll('[marker-start], [marker-end]').forEach((n) => {
            n.removeAttribute('marker-start');
            n.removeAttribute('marker-end');
        });
        for (const id of ids) {
            el.svgHost.querySelectorAll(`[data-if="${id}"]`).forEach((n) => n.classList.add('hl'));
        }
        if (!diagram.pairLayer) return;
        diagram.pairLayer.replaceChildren();

        const arrow = 'url(#topo-arrow)';
        const linkOf = (id) => el.svgHost.querySelector(`path.link[data-if="${id}"]`);

        if (ids.length === 1) {
            const link = linkOf(ids[0]); // none for 0 (local)
            if (link) link.setAttribute(dir === 'ingress' ? 'marker-start' : 'marker-end', arrow);
            return;
        }
        if (ids.length !== 2 || ids[0] === ids[1]) return;

        const [ingress, egress] = ids;
        const ya = diagram.y.get(ingress);
        const yb = diagram.y.get(egress);
        if (ya === undefined || yb === undefined) return;
        // Bracket-shaped curve to the right of the interface column, drawn from ingress to egress
        const x = diagram.x + L.ifR;
        const bulge = Math.min(40 + Math.abs(yb - ya) * 0.2, 90);
        const path = svgEl('path', {
            d: `M${x},${ya} C${x + bulge},${ya} ${x + bulge},${yb} ${x},${yb}`,
            class: 'pair-link',
        });
        diagram.pairLayer.appendChild(path);

        const egressLink = linkOf(egress);
        if (egressLink) egressLink.setAttribute('marker-end', arrow);
        else path.setAttribute('marker-end', arrow); // egress is 0 (local)
    }

    function renderSummary() {
        const fmt = (d) => d.toLocaleString(undefined, { dateStyle: 'medium', timeStyle: 'medium', timeZone: 'UTC' });
        const d = state.defaults;
        const lines = [
            `Valid from ${fmt(state.validFrom)} to ${fmt(state.validUntil)} (UTC).`,
            `Allocation ${MODE_TEXT[state.mode]}.`,
            `Redeemable assets: ${formatKbps(d.bandwidth_min)} to ${formatKbps(d.bandwidth_max)}, ` +
            `${formatDuration(d.time_min_duration)} to ${formatDuration(d.time_max_duration)} ` +
            `in steps of ${formatDuration(d.time_granularity)}.`,
        ];
        if (state.prefillNote) lines.push(state.prefillNote);
        el.bwSummary.replaceChildren(...lines.map((t) => {
            const p = document.createElement('p');
            p.textContent = t;
            return p;
        }));
    }

    // -------------------------------------------------------------- bandwidth

    // Entries: pairs {ingress, egress, kbps, price}; per interface {interface, direction, kbps, price}.
    // price is per kbit/s per second; null until set (e.g. for entries pre-filled from static info).
    function keyOf(e) {
        return e.interface !== undefined ? `i:${e.direction}:${e.interface}` : pairKey(e.ingress, e.egress);
    }

    function pairKey(ingress, egress) {
        return `p:${ingress}>${egress}`;
    }

    function entryIds(e) {
        return e.interface !== undefined ? [e.interface] : [e.ingress, e.egress];
    }

    // Per interface: by interface, ingress before egress.
    // Pairs: both directions of a pair next to each other, lower ingress first.
    function compareEntries(x, y) {
        if (x.interface !== undefined) {
            return x.interface - y.interface || (x.direction === 'ingress' ? 0 : 1) - (y.direction === 'ingress' ? 0 : 1);
        }
        const lo = (e) => Math.min(e.ingress, e.egress);
        const hi = (e) => Math.max(e.ingress, e.egress);
        return lo(x) - lo(y) || hi(x) - hi(y) || x.ingress - y.ingress;
    }

    function ifcText(id) {
        return id === LOCAL ? '0 (local)' : String(id);
    }

    function directionText(dir) {
        return dir === 'ingress' ? 'Ingress' : 'Egress';
    }

    function entryText(e) {
        return e.interface !== undefined
            ? `interface ${ifcText(e.interface)} ${e.direction}`
            : `${e.ingress} → ${e.egress}`;
    }

    function setupBandwidthForm() {
        const pairs = state.mode === 'pairs';
        const ids = state.interfaces.map((ifc) => ifc.id);
        // "0 (local)" is selectable in both modes
        const options = () => [LOCAL, ...ids].map((id) => new Option(ifcText(id), id));

        el.bwALabel.textContent = pairs ? 'Interface 1' : 'Interface';
        el.bwA.replaceChildren(...options());
        el.bwA.value = String(ids[0]);
        el.bwBWrap.hidden = !pairs;
        el.bwBaWrap.hidden = !pairs;
        el.bwDirWrap.hidden = pairs;
        el.bwValueRev.required = pairs;
        el.bwPriceRev.required = pairs;
        if (pairs) {
            el.bwB.replaceChildren(...options());
            el.bwB.value = String(ids.length > 1 ? ids[1] : LOCAL);
        } else {
            el.bwB.replaceChildren();
            el.bwDir.value = 'ingress';
        }
        clearValueFields();

        const cols = pairs
            ? ['Ingress', 'Egress', 'Bandwidth', 'Price', '']
            : ['Interface', 'Direction', 'Bandwidth', 'Price', ''];
        const tr = document.createElement('tr');
        for (const c of cols) {
            const th = document.createElement('th');
            th.textContent = c;
            if (c === 'Price') th.title = 'Per kbit/s per second';
            tr.appendChild(th);
        }
        el.bwTableHead.replaceChildren(tr);
        onPairChanged();
    }

    function clearValueFields() {
        for (const n of [el.bwValue, el.bwValueRev, el.bwPrice, el.bwPriceRev]) n.value = '';
    }

    // Pairs: the interface chosen on one side is disabled on the other side,
    // so Interface 1 and Interface 2 can never be the same.
    function syncPairOptions() {
        if (el.bwA.value === el.bwB.value) {
            const other = [...el.bwB.options].find((o) => o.value !== el.bwA.value);
            if (other) el.bwB.value = other.value;
        }
        for (const o of el.bwA.options) o.disabled = o.value === el.bwB.value;
        for (const o of el.bwB.options) o.disabled = o.value === el.bwA.value;
    }

    // Current form selection; per interface: {a, dir}, pairs: {a: interface 1, b: interface 2}
    function selected() {
        const a = Number(el.bwA.value);
        return state.mode === 'pairs' ? { a, b: Number(el.bwB.value) } : { a, b: null, dir: el.bwDir.value };
    }

    function onPairChanged() {
        if (state.mode === 'pairs') syncPairOptions();
        const { a, b, dir } = selected();
        let exists;
        if (b === null) {
            highlight([a], dir);
            el.bwAbLabel.textContent = `${directionText(dir)} bandwidth`;
            el.bwPriceLabel.textContent = `${directionText(dir)} price`;
            exists = state.bandwidths.has(keyOf({ interface: a, direction: dir }));
        } else {
            // The arrow shows the direction whose fields are being edited; 1 → 2 by default
            const f = document.activeElement;
            if ([el.bwValueRev, el.bwUnitRev, el.bwPriceRev].includes(f)) highlight([b, a]);
            else highlight([a, b]);
            el.bwAbLabel.textContent = `Bandwidth ${a} → ${b}`;
            el.bwPriceLabel.textContent = `Price ${a} → ${b}`;
            el.bwBaLabel.textContent = `Bandwidth ${b} → ${a}`;
            el.bwPriceRevLabel.textContent = `Price ${b} → ${a}`;
            exists = state.bandwidths.has(pairKey(a, b)) || state.bandwidths.has(pairKey(b, a));
        }
        el.bwSubmit.textContent = exists ? 'Update' : 'Add';
    }

    // Puts stored values into the form: a pair in both directions, or one interface direction.
    function loadIntoForm(e) {
        const fill = (value, unit, price, key) => {
            const found = state.bandwidths.get(key);
            if (found && found.kbps > 0) setWithUnit(value, unit, found.kbps, BW_UNITS);
            else value.value = '';
            price.value = found && found.price !== null ? String(found.price) : '';
        };
        if (e.interface !== undefined) {
            el.bwA.value = String(e.interface);
            el.bwDir.value = e.direction;
            onPairChanged();
            fill(el.bwValue, el.bwUnit, el.bwPrice, keyOf(e));
            (el.bwValue.value === '' ? el.bwValue : el.bwPrice).focus();
        } else {
            el.bwA.value = String(e.ingress);
            el.bwB.value = String(e.egress);
            onPairChanged();
            fill(el.bwValue, el.bwUnit, el.bwPrice, pairKey(e.ingress, e.egress));
            fill(el.bwValueRev, el.bwUnitRev, el.bwPriceRev, pairKey(e.egress, e.ingress));
            const firstEmpty = [el.bwValue, el.bwPrice, el.bwValueRev, el.bwPriceRev].find((n) => n.value === '');
            (firstEmpty || el.bwValue).focus();
        }
    }

    function readKbps(input, unit, what) {
        const v = parseFloat(input.value);
        const kbps = Math.round(v * Number(unit.value));
        if (!(v > 0) || kbps < 1) {
            input.focus();
            throw new Error(`${what}: please enter a bandwidth of at least 1 kbit/s.`);
        }
        if (kbps > MAX_U32) {
            input.focus();
            throw new Error(`${what}: the bandwidth must be at most ${formatKbps(MAX_U32)}.`);
        }
        return kbps;
    }

    function readPrice(input, what) {
        const raw = input.value.trim();
        const v = Number(raw);
        if (raw === '' || !Number.isInteger(v) || v < 0 || v > MAX_U32) {
            input.focus();
            throw new Error(`${what}: please enter the price as a whole number between 0 and ${MAX_U32}.`);
        }
        return v;
    }

    function onAddBandwidth(ev) {
        ev.preventDefault();
        clearError();
        const { a, b, dir } = selected();
        try {
            if (b === null) {
                const what = `Interface ${a} ${dir}`;
                const e = {
                    interface: a, direction: dir,
                    kbps: readKbps(el.bwValue, el.bwUnit, what), price: readPrice(el.bwPrice, what),
                };
                state.bandwidths.set(keyOf(e), e);
            } else {
                if (a === b) throw new Error('Interface 1 and Interface 2 must be different.');
                // Both directions are read before anything is stored.
                const ab = {
                    ingress: a, egress: b,
                    kbps: readKbps(el.bwValue, el.bwUnit, `${a} → ${b}`), price: readPrice(el.bwPrice, `${a} → ${b}`),
                };
                const ba = {
                    ingress: b, egress: a,
                    kbps: readKbps(el.bwValueRev, el.bwUnitRev, `${b} → ${a}`),
                    price: readPrice(el.bwPriceRev, `${b} → ${a}`),
                };
                state.bandwidths.set(keyOf(ab), ab);
                state.bandwidths.set(keyOf(ba), ba);
            }
        } catch (err) {
            showError(err.message);
            return;
        }
        clearValueFields();
        renderBandwidths();
        onPairChanged();
    }

    // Directions that still need a value (shown as a hint, they do not block submitting).
    //   Pairs: the reverse of every configured direction, and 0 → x / x → 0 for every interface x used in a pair.
    //   Per interface: the other direction (ingress / egress) of every configured interface.
    function missingDirections() {
        if (state.mode !== 'pairs') {
            const need = new Map();
            for (const e of state.bandwidths.values()) {
                const other = { interface: e.interface, direction: e.direction === 'ingress' ? 'egress' : 'ingress' };
                const k = keyOf(other);
                if (!state.bandwidths.has(k)) need.set(k, other);
            }
            return [...need.values()].sort(compareEntries);
        }
        const need = new Map();
        const want = (ingress, egress) => {
            const k = pairKey(ingress, egress);
            if (!state.bandwidths.has(k)) need.set(k, { ingress, egress });
        };
        for (const e of state.bandwidths.values()) {
            want(e.egress, e.ingress);
            for (const id of [e.ingress, e.egress]) {
                if (id !== LOCAL) {
                    want(LOCAL, id);
                    want(id, LOCAL);
                }
            }
        }
        return [...need.values()].sort(compareEntries);
    }

    // Entries sent to the server: everything with a bandwidth. Entries left at 0 kbit/s
    // (e.g. pre-filled with ratio 0%) are skipped.
    function submittedEntries() {
        return [...state.bandwidths.values()].filter((e) => e.kbps > 0).sort(compareEntries);
    }

    function entriesWithoutPrice() {
        return submittedEntries().filter((e) => e.price === null);
    }

    function formatKbps(kbps) {
        for (const [f, name] of BW_UNITS) {
            if (kbps >= f) return `${+(kbps / f).toFixed(3)} ${name}`;
        }
        return `${kbps} kbit/s`;
    }

    function formatDuration(s) {
        for (const [f, name] of TIME_UNITS) {
            if (s >= f && s % f === 0) return `${s / f} ${name}`;
        }
        return `${s} s`;
    }

    // One table row per configured direction.
    function renderBandwidths() {
        setStatus(''); // a previous Create result no longer matches the table
        const rows = [...state.bandwidths.values()].sort(compareEntries).map((e) => {
            const price = e.price === null ? { text: 'not set', cls: 'unset' } : String(e.price);
            const bw = e.kbps === 0 ? { text: formatKbps(0), cls: 'unset' } : formatKbps(e.kbps);
            const cells = e.interface !== undefined
                ? [ifcText(e.interface), directionText(e.direction), bw, price]
                : [ifcText(e.ingress), ifcText(e.egress), bw, price];
            return tableRow(cells,
                () => highlight(entryIds(e), e.direction), // pairs: [ingress, egress]; per interface: its direction
                () => loadIntoForm(e),
                () => state.bandwidths.delete(keyOf(e)));
        });
        el.bwTableBody.replaceChildren(...rows);

        const empty = rows.length === 0;
        el.bwTable.hidden = empty;
        el.bwEmpty.hidden = !empty;

        const missing = missingDirections();
        el.bwMissing.hidden = missing.length === 0;
        el.bwMissingList.replaceChildren(...missing.map((m) => {
            if (m.interface !== undefined) {
                const b = button(`${ifcText(m.interface)} ${m.direction}`, 'secondary', () => loadIntoForm(m));
                b.title = `Interface ${ifcText(m.interface)}, ${m.direction}: fill into the form`;
                return b;
            }
            const b = button(`${m.ingress} → ${m.egress}`, 'secondary', () => loadIntoForm(m));
            b.title = `Ingress ${ifcText(m.ingress)}, egress ${ifcText(m.egress)}: fill into the form`;
            return b;
        }));
    }

    // cells: text, or {text, cls} for a styled cell
    function tableRow(cells, onHover, onEdit, onRemove) {
        const tr = document.createElement('tr');
        for (const c of cells) {
            const td = document.createElement('td');
            if (typeof c === 'object') {
                td.textContent = c.text;
                td.className = c.cls;
            } else {
                td.textContent = c;
            }
            tr.appendChild(td);
        }
        const actions = document.createElement('td');
        actions.className = 'row-actions';
        actions.append(
            button('Edit', 'secondary', onEdit),
            button('Remove', 'danger', () => {
                onRemove();
                renderBandwidths();
                onPairChanged();
            }));
        tr.appendChild(actions);
        tr.addEventListener('mouseenter', onHover);
        tr.addEventListener('mouseleave', onPairChanged);
        return tr;
    }

    function button(text, cls, onClick) {
        const b = document.createElement('button');
        b.type = 'button';
        b.className = cls;
        b.textContent = text;
        b.addEventListener('click', onClick);
        return b;
    }

    // ------------------------------------------------------ request

    // Matches setupRequest on the server (plus "mode"):
    // times as Unix seconds, bandwidths in kbit/s, durations in seconds, price per kbit/s per second.
    // Per-interface entries use 0 on the unused side: ingress x → {ingress: x, egress: 0},
    // egress x → {ingress: 0, egress: x}.
    function configObject() {
        if (!state.defaults) return null;
        return {
            starts_at: Math.floor(state.validFrom.getTime() / 1000),
            stops_at: Math.floor(state.validUntil.getTime() / 1000),
            base: { ...state.defaults },
            mode: state.mode,
            bandwidths: submittedEntries().map((e) => {
                if (e.interface === undefined) {
                    return { ingress: e.ingress, egress: e.egress, kbps: e.kbps, price: e.price };
                }
                return e.direction === 'ingress'
                    ? { ingress: e.interface, egress: LOCAL, kbps: e.kbps, price: e.price }
                    : { ingress: LOCAL, egress: e.interface, kbps: e.kbps, price: e.price };
            }),
        };
    }

    // Problems that should block the request; empty when complete.
    // Missing directions (see missingDirections) and entries at 0 kbit/s do not block:
    // the latter are simply not submitted.
    function validate() {
        if (!state.defaults) return ['Setup has not been completed.'];
        const problems = [];
        if (submittedEntries().length === 0) {
            problems.push(state.bandwidths.size === 0
                ? 'No bandwidth configured.'
                : 'No bandwidth configured: entries with 0 kbit/s are not submitted.');
        }
        const noPrice = entriesWithoutPrice();
        if (noPrice.length) problems.push(`Price not set for ${noPrice.map(entryText).join(', ')}.`);
        return problems;
    }

    const CREATE_URL = '/assetup/create';
    const SUCCESS_URL = '/asassets';

    async function onCreate() {
        setStatus('');
        const problems = validate();
        if (problems.length) {
            setStatus(problems.join(' '), 'error');
            return;
        }
        const payload = { ...configObject(), check_overlap: !el.skipOverlap.checked };

        el.create.disabled = true;
        el.create.textContent = 'Creating…';
        let leaving = false; // keep the button disabled while the browser navigates away
        try {
            const res = await fetch(CREATE_URL, {
                method: 'POST',
                headers: { 'Content-Type': 'application/json', 'Accept': 'application/json' },
                credentials: 'same-origin',
                body: JSON.stringify(payload),
            });
            if (res.redirected) {
                leaving = true;
                window.location.href = res.url;
                return;
            }
            if (!res.ok) {
                setStatus(await responseError(res), 'error');
                return;
            }
            leaving = true;
            setStatus('Setup created. Opening assets…', 'success');
            window.location.href = SUCCESS_URL;
        } catch (err) {
            setStatus(`Could not reach the server: ${err.message}`, 'error');
        } finally {
            if (!leaving) {
                el.create.disabled = false;
                el.create.textContent = 'Create';
            }
        }
    }

    // Best readable message from an error response: a JSON "error"/"message" field, else short plain text.
    async function responseError(res) {
        const fallback = `Request failed (${res.status}${res.statusText ? ' ' + res.statusText : ''}).`;
        let text = '';
        try {
            text = (await res.text()).trim();
        } catch (_) {
            return fallback;
        }
        if ((res.headers.get('Content-Type') || '').includes('application/json')) {
            try {
                const body = JSON.parse(text);
                const msg = body && (body.error || body.message);
                if (typeof msg === 'string' && msg) return msg;
            } catch (_) { /* fall through */ }
        }
        if (text && !text.startsWith('<') && text.length <= 300) return text;
        return fallback;
    }

    function setStatus(msg, kind) {
        el.submitStatus.textContent = msg;
        el.submitStatus.className = kind || '';
    }

    // ------------------------------------------------------------ page state

    function showView() {
        el.dialog.hidden = true;
        el.view.hidden = false;
        el.bwSection.hidden = false;
    }

    function showDialog() {
        clearError();
        formError('');
        el.view.hidden = true;
        el.bwSection.hidden = true;
        el.dialog.hidden = false;
        el.dialog.scrollIntoView({ block: 'start' });
    }

    function formError(msg, field) {
        el.formError.textContent = msg;
        if (field) {
            field.setAttribute('aria-invalid', 'true');
            field.focus();
        }
    }

    // Errors of the bandwidth form, shown right below it
    function showError(msg) {
        el.bwError.textContent = msg;
    }

    function clearError() {
        el.bwError.textContent = '';
        el.error.textContent = '';
    }

    // For the later request: getConfig() returns the object above, validate() lists what is missing.
    window.setupAssets = { getConfig: configObject, validate };
})();


// ===================================================================== 5. Table filters

// A <form class="filter-form" data-filter-table="<table id>"> filters the rows of that table that carry
// data-filter-row. Each row describes itself with data-ia, data-ingress, data-egress, data-starts-at and
// data-stops-at (unix seconds) and data-bandwidth. Empty fields and filters without an input do not filter:
//   ia               the row's ISD-AS contains the given text (case-insensitive), e.g. "ff00:0:110"
//   ingress, egress  exact match (a row without the interface never matches)
//   starts_at        start of the time window (UTC): the row must stop at or after it
//   stops_at         end of the time window (UTC): the row must start at or before it
//                    i.e. rows overlapping the window are shown (row.start <= end, row.end >= start)
//   bandwidth        row bandwidth is at least the given value
// The filter values are kept in the URL query, so they survive reloads and switching the account.
(function () {
    'use strict';

    const num = (raw) => {
        const v = Number(raw);
        return raw === '' || !Number.isFinite(v) ? null : v;
    };
    // datetime-local value, read as UTC like the times shown in the tables -> unix seconds
    const time = (raw) => {
        const ms = raw === '' ? NaN : Date.parse(raw + 'Z');
        return Number.isNaN(ms) ? null : ms / 1000;
    };

    const text = (raw) => (raw === '' ? null : raw.toLowerCase());

    // Filter name (= input name and URL parameter) ->
    //   attr:  data attribute of the row to compare with (dataset key)
    //   parse: parses the input value and the row value (null = not set)
    //   test:  (row value, filter value) -> whether the row matches
    // The time window is an overlap: its start is compared with the row's end and its end with the row's start.
    const FILTERS = {
        ia: { attr: 'ia', parse: text, test: (row, v) => row.includes(v) },
        ingress: { attr: 'ingress', parse: num, test: (row, v) => row === v },
        egress: { attr: 'egress', parse: num, test: (row, v) => row === v },
        starts_at: { attr: 'stopsAt', parse: time, rowParse: num, test: (row, v) => row >= v },
        stops_at: { attr: 'startsAt', parse: time, rowParse: num, test: (row, v) => row <= v },
        bandwidth: { attr: 'bandwidth', parse: num, test: (row, v) => row >= v },
    };

    document.addEventListener('DOMContentLoaded', () => {
        document.querySelectorAll('form[data-filter-table]').forEach(setup);
    });

    function setup(form) {
        const table = document.getElementById(form.dataset.filterTable);
        if (!table) return;
        const count = form.parentElement.querySelector('[data-filter-count]');

        const params = new URLSearchParams(window.location.search);
        for (const name of Object.keys(FILTERS)) {
            const input = form.elements[name];
            if (input && params.has(name)) input.value = params.get(name);
        }

        const run = () => apply(form, table, count);
        form.addEventListener('input', run);
        form.addEventListener('submit', (ev) => {
            ev.preventDefault();
            run();
        });
        form.addEventListener('reset', () => setTimeout(run)); // after the browser cleared the fields
        run();
    }

    function apply(form, table, count) {
        const active = [];
        const params = new URLSearchParams(window.location.search);
        for (const [name, filter] of Object.entries(FILTERS)) {
            const input = form.elements[name];
            if (!input) continue;
            const raw = input.value.trim();
            const value = filter.parse(raw);
            if (value === null) {
                params.delete(name);
                continue;
            }
            params.set(name, raw);
            active.push({ filter, value });
        }
        const query = params.toString();
        window.history.replaceState(null, '', window.location.pathname + (query ? '?' + query : '') + window.location.hash);

        const rows = [...table.querySelectorAll('tbody tr[data-filter-row]')];
        let shown = 0;
        for (const row of rows) {
            const match = active.every(({ filter, value }) => {
                const v = (filter.rowParse || filter.parse)((row.dataset[filter.attr] || '').trim());
                return v !== null && filter.test(v, value);
            });
            row.hidden = !match;
            if (match) shown++;
        }

        // Message row when the filters hide every row
        let none = table.querySelector('tbody tr.filter-none');
        if (!none && rows.length) {
            none = document.createElement('tr');
            none.className = 'filter-none';
            const td = document.createElement('td');
            td.className = 'empty';
            td.colSpan = table.querySelectorAll('thead th').length || 1;
            td.textContent = 'No entries match the filters.';
            none.appendChild(td);
            table.tBodies[0].appendChild(none);
        }
        if (none) none.hidden = !(rows.length && shown === 0);

        if (count) {
            count.textContent = active.length ? `Showing ${shown} of ${rows.length} entries.` : '';
        }
    }
})();
