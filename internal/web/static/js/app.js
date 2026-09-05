// NoTalk — minimal client-side JavaScript
// Most interactivity is handled by htmx + Alpine.js

document.addEventListener('DOMContentLoaded', function() {
  // ── htmx global events ──

  // Show a global indicator on long requests
  document.body.addEventListener('htmx:beforeRequest', function() {
    document.body.classList.add('htmx-loading');
  });
  document.body.addEventListener('htmx:afterRequest', function() {
    document.body.classList.remove('htmx-loading');
  });

  // Handle htmx errors with a toast
  document.body.addEventListener('htmx:responseError', function(e) {
    var msg = e.detail.xhr?.responseText || 'Something went wrong';
    showToast('error', msg);
  });

  // ── Button loading states ──
  // Any form with a .btn-loading button will show a spinner on submit
  document.addEventListener('submit', function(e) {
    var btn = e.target.querySelector('.btn-loading');
    if (btn && !btn.disabled) {
      btn.disabled = true;
      var spinner = btn.querySelector('.btn-spinner');
      if (spinner) spinner.classList.remove('hidden');
      // Re-enable after 8s as a safety fallback
      setTimeout(function() {
        btn.disabled = false;
        if (spinner) spinner.classList.add('hidden');
      }, 8000);
    }
  });

  // ── Scroll-triggered animations ──
  initScrollAnimations();
});

// Scroll animations (IntersectionObserver)
function initScrollAnimations() {
  var els = document.querySelectorAll('.anim-on-scroll');
  if (!els.length) return;
  if (!('IntersectionObserver' in window)) {
    els.forEach(function(el) { el.classList.add('is-visible'); });
    return;
  }
  var observer = new IntersectionObserver(function(entries) {
    entries.forEach(function(entry) {
      if (entry.isIntersecting) {
        entry.target.classList.add('is-visible');
        observer.unobserve(entry.target);
      }
    });
  }, { threshold: 0.1 });
  els.forEach(function(el) { observer.observe(el); });
}

// Simple toast notification (for JS-triggered messages)
function showToast(type, message) {
  var colors = {
    success: 'bg-emerald-50 text-emerald-800 border-emerald-200',
    error: 'bg-red-50 text-red-800 border-red-200',
    info: 'bg-blue-50 text-blue-800 border-blue-200',
  };
  var toast = document.createElement('div');
  toast.className = 'fixed top-4 right-4 z-[100] px-4 py-3 rounded-xl text-sm border shadow-lg toast-enter max-w-sm ' + (colors[type] || colors.info);
  toast.textContent = message;
  document.body.appendChild(toast);
  setTimeout(function() {
    toast.style.opacity = '0';
    toast.style.transform = 'translateX(16px)';
    toast.style.transition = 'opacity 0.3s, transform 0.3s';
    setTimeout(function() { toast.remove(); }, 300);
  }, 4000);
}
