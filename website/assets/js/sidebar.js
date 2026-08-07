"use strict";

/* ====== Sidebar navigation generator ======

   Builds the contents of #docs-nav's <ul class="section-items"> from the
   document itself, so the sidebar can't drift out of sync with the page.
   Add a section to the page and it shows up in the sidebar; no second edit.

   This must run *before* docs.js, which grabs the sidebar links and hands
   #docs-nav to Gumshoe at parse time.

   Heading levels map to sidebar levels directly and unconditionally:

     <h1 class="docs-heading">   section title
     <h2>                        nav item
     <h3>                        submenu item under the preceding <h2>
     <h4> and deeper             not listed

   That mapping is deliberately rigid. The sidebar is meant to be a faithful
   picture of the document's heading structure, so a page whose headings skip
   or invert levels produces a visibly wrong sidebar rather than a tidy one --
   an <h3> with no <h2> before it in its article is drawn indented under
   nothing, and every such problem is also reported through console.warn.
   Fix the headings, not the sidebar.

   Expected page structure:

     <article class="docs-article" id="section-stars" data-nav-icon="svg:#radar">
       <h1 class="docs-heading">STARS</h1>
       <section class="docs-section" id="stars-dcb">
         <h2>The DCB</h2>
         <h3 id="dcb-aux">Aux Menu</h3>
       </section>
     </article>

   A heading's anchor is its own id if it has one, otherwise the id of the
   enclosing .docs-section when the heading is that section's first. Headings
   with neither get a slug generated from their text.

   Attributes, all declared in the HTML:

     data-nav-icon="fas fa-wrench"  On an article: the icon for its section
                                    title. Use "svg:#radar" to reference a
                                    sprite symbol instead of a Font Awesome
                                    class.
     data-nav-title="Mac"           Text to use in the sidebar in place of the
                                    heading's own text, for headings too long
                                    to read well in a narrow column. On a
                                    non-heading element it also *creates* an
                                    entry, which is how an anchor that isn't a
                                    heading (#release-earlier) gets listed.
     data-nav-level="2"|"3"         Required on those non-heading entries,
                                    since there's no tag to take a level from.
                                    Not for use on headings: a heading at the
                                    wrong level is a bug in the page.
     data-nav-skip                  Leave this heading (or article) out.
     data-nav-skip-children         Leave every entry *inside* this element out.
                                    Used on the collapsed older-releases block
                                    so ~80 release headings stay out of the nav.
*/

(function () {
    const nav = document.getElementById('docs-nav');
    if (!nav) return;

    const list = nav.querySelector('ul.section-items');
    if (!list) return;

    const problems = [];

    /* Every id already in the document, so generated ones don't collide. */
    const takenIds = new Set();
    document.querySelectorAll('[id]').forEach((el) => takenIds.add(el.id));

    function slugify(text) {
        return text
            .toLowerCase()
            .replace(/[^a-z0-9]+/g, '-')
            .replace(/^-+|-+$/g, '');
    }

    /* Heading text as it should read in the sidebar. Icon markup (<i class=...>,
       <svg>) is dropped; a bare <i> is italics, not an icon, so it stays. */
    function navText(el) {
        if (el.dataset.navTitle !== undefined) return el.dataset.navTitle;
        const copy = el.cloneNode(true);
        copy.querySelectorAll('i[class], svg, .theme-icon-holder').forEach((n) => n.remove());
        return copy.textContent.replace(/\s+/g, ' ').trim();
    }

    function uniqueId(base) {
        let id = base || 'section';
        for (let n = 2; takenIds.has(id); n++) id = base + '-' + n;
        takenIds.add(id);
        return id;
    }

    /* Where this entry should link to, assigning an id to the element if it
       doesn't already have a usable anchor. */
    function anchorFor(el) {
        if (el.id) return el.id;

        const section = el.closest('section.docs-section');
        if (section && section.id && section.querySelector('h1, h2, h3, h4, h5, h6') === el)
            return section.id;

        el.id = uniqueId(slugify(navText(el)));
        return el.id;
    }

    function levelOf(el) {
        const m = /^H([1-6])$/.exec(el.tagName);
        if (m) return parseInt(m[1], 10);
        return el.dataset.navLevel ? parseInt(el.dataset.navLevel, 10) : NaN;
    }

    /* Entries for one article, in document order. */
    function entriesIn(article) {
        const entries = [];

        article.querySelectorAll('h2, h3, h4, h5, h6, [data-nav-title]').forEach((el) => {
            if (el.hasAttribute('data-nav-skip')) return;
            if (el.parentElement && el.parentElement.closest('[data-nav-skip-children]')) return;

            const level = levelOf(el);
            if (level !== 2 && level !== 3) return;

            const title = navText(el);
            if (!title) return;

            entries.push({ level: level, title: title, id: anchorFor(el), el: el });
        });

        return entries;
    }

    function link(className, id, title) {
        const a = document.createElement('a');
        a.className = className + ' scrollto';
        a.href = '#' + id;
        a.textContent = title;
        return a;
    }

    function iconHolder(spec) {
        const span = document.createElement('span');
        span.className = 'theme-icon-holder me-2';

        if (spec.indexOf('svg:') === 0) {
            const SVG_NS = 'http://www.w3.org/2000/svg';
            const svg = document.createElementNS(SVG_NS, 'svg');
            const use = document.createElementNS(SVG_NS, 'use');
            use.setAttributeNS('http://www.w3.org/1999/xlink', 'xlink:href', spec.slice(4));
            use.setAttribute('href', spec.slice(4));
            svg.appendChild(use);
            span.appendChild(svg);
        } else {
            const i = document.createElement('i');
            i.className = spec;
            span.appendChild(i);
        }

        return span;
    }

    function submenuItem(entry) {
        const item = document.createElement('li');
        item.className = 'submenu-item';
        item.appendChild(link('submenu-link', entry.id, entry.title));
        return item;
    }

    /* Build into a fragment and swap it in at the very end, so that if anything
       here throws we leave the markup that's already on the page alone. */
    const fragment = document.createDocumentFragment();
    let sectionCount = 0;

    document.querySelectorAll('article.docs-article').forEach((article) => {
        if (article.hasAttribute('data-nav-skip')) return;

        const heading = article.querySelector('h1.docs-heading');
        if (!heading) {
            problems.push('<article' + (article.id ? ' id="' + article.id + '"' : '') +
                          '> has no <h1 class="docs-heading">; the whole article is missing ' +
                          'from the sidebar.');
            return;
        }

        if (!article.id) article.id = uniqueId(slugify(navText(heading)));

        const titleItem = document.createElement('li');
        titleItem.className = 'nav-item section-title' + (sectionCount++ ? ' mt-3' : '');

        const titleLink = link('nav-link', article.id, navText(heading));
        if (article.dataset.navIcon)
            titleLink.insertBefore(iconHolder(article.dataset.navIcon), titleLink.firstChild);

        titleItem.appendChild(titleLink);
        fragment.appendChild(titleItem);

        /* <h3>s attach to the <h2> that most recently preceded them. One that
           turns up before any <h2> in its article has nothing to attach to; it
           gets drawn indented under nothing so the gap is visible on the page. */
        let submenu = null;
        let orphans = null;

        entriesIn(article).forEach((entry) => {
            if (entry.level === 3) {
                if (!submenu) {
                    if (!orphans) {
                        orphans = document.createElement('ul');
                        orphans.className = 'submenu';
                        fragment.appendChild(orphans);
                    }
                    problems.push('#' + article.id + ': <h3> "' + entry.title +
                                  '" has no <h2> before it in this article, so it has no parent ' +
                                  'in the sidebar.');
                    orphans.appendChild(submenuItem(entry));
                    return;
                }
                submenu.appendChild(submenuItem(entry));
                return;
            }

            orphans = null;

            const item = document.createElement('li');
            item.className = 'nav-item';
            item.appendChild(link('nav-link', entry.id, entry.title));

            submenu = document.createElement('ul');
            submenu.className = 'submenu';
            item.appendChild(submenu);

            fragment.appendChild(item);
        });

        /* Drop the submenus that ended up with nothing in them. */
        fragment.querySelectorAll('ul.submenu:empty').forEach((ul) => ul.remove());
    });

    list.innerHTML = '';
    list.appendChild(fragment);

    if (problems.length && window.console && console.warn)
        console.warn('sidebar.js: heading structure problems\n  ' + problems.join('\n  '));
})();
