// OfflinePay — Premium Frontend Interactions (GSAP + ScrollTrigger)

document.addEventListener("DOMContentLoaded", () => {
    // Register ScrollTrigger plugin
    gsap.registerPlugin(ScrollTrigger);

    initNavbarScroll();
    initHeroAnimations();
    initMetricsCounters();
    initCapabilitiesCards();
    initArchitectureFlow();
    initSecurityChecklist();
    initLedgerAnimation();
    initTimelineReveal();
    initDashboardAnimation();
    initAchievementsReveal();
});

// 1. Sticky Navbar styling change on scroll
function initNavbarScroll() {
    const navbar = document.getElementById("main-nav");
    
    window.addEventListener("scroll", () => {
        if (window.scrollY > 50) {
            navbar.classList.add("scrolled");
        } else {
            navbar.classList.remove("scrolled");
        }
    });
}

// 2. Hero Section Entrance Animations
function initHeroAnimations() {
    // Fade up titles and buttons sequentially
    gsap.to(".hero-text-side .reveal-fade-up", {
        opacity: 1,
        y: 0,
        duration: 1.2,
        stagger: 0.15,
        ease: "power3.out",
        delay: 0.2
    });

    // Scale and fade in the hero visual pillar representation
    gsap.to(".hero-visual-side.reveal-scale-in", {
        opacity: 1,
        scale: 1,
        duration: 1.6,
        ease: "power2.out",
        delay: 0.4
    });
}

// 3. Metrics Number Counter (Section 2)
function initMetricsCounters() {
    const numElements = document.querySelectorAll(".proof-number");

    numElements.forEach((num) => {
        const target = parseFloat(num.getAttribute("data-target"));
        const decimals = parseInt(num.getAttribute("data-decimals"), 10) || 0;
        const suffix = num.getAttribute("data-suffix") || "";

        // Set initial state
        gsap.set(num, { opacity: 0, y: 15 });

        // Fade in item
        gsap.to(num, {
            opacity: 1,
            y: 0,
            duration: 1,
            ease: "power2.out",
            scrollTrigger: {
                trigger: num,
                start: "top 90%"
            }
        });

        // Animate count-up
        let counterObj = { value: 0 };
        gsap.to(counterObj, {
            value: target,
            duration: 2.2,
            ease: "power2.out",
            scrollTrigger: {
                trigger: num,
                start: "top 85%",
                toggleActions: "play none none none"
            },
            onUpdate: () => {
                num.textContent = counterObj.value.toFixed(decimals).replace(/\B(?=(\d{3})+(?!\d))/g, ",") + suffix;
            }
        });
    });
}

// 4. Core Capabilities Card Morphing/Slight Scale (Section 3)
function initCapabilitiesCards() {
    const cards = document.querySelectorAll(".reveal-card-morph");

    cards.forEach((card, idx) => {
        gsap.fromTo(card, 
            { opacity: 0, y: 40, scale: 0.98 },
            {
                opacity: 1,
                y: 0,
                scale: 1,
                duration: 1,
                ease: "power2.out",
                scrollTrigger: {
                    trigger: card,
                    start: "top 88%",
                    toggleActions: "play none none none"
                },
                delay: (idx % 4) * 0.1 // stagger desktop grid items
            }
        );
    });
}

// 5. Interactive Architecture Flow System (Section 4)
function initArchitectureFlow() {
    // Animate flow lines in sequence using scroll trigger scrub
    const wires = document.querySelectorAll(".arch-wire");
    
    wires.forEach((wire) => {
        // Compute path length
        const length = wire.getTotalLength();
        // Set up dashed dasharray offsets
        gsap.set(wire, { strokeDasharray: length, strokeDashoffset: length });

        gsap.to(wire, {
            strokeDashoffset: 0,
            duration: 1.5,
            ease: "power1.inOut",
            scrollTrigger: {
                trigger: ".architecture-visualizer",
                start: "top 60%",
                end: "bottom 80%",
                scrub: 1
            }
        });
    });

    // Stagger activation of node boxes on reveal
    const nodes = document.querySelectorAll(".arch-node");
    nodes.forEach((node, idx) => {
        gsap.fromTo(node,
            { opacity: 0, scale: 0.92 },
            {
                opacity: 1,
                scale: 1,
                duration: 0.8,
                ease: "power2.out",
                scrollTrigger: {
                    trigger: ".architecture-visualizer",
                    start: "top 70%",
                    toggleActions: "play none none none"
                },
                delay: idx * 0.12
            }
        );
    });
}

// 6. Security Checklist & Shield Staggered Reveals (Section 5)
function initSecurityChecklist() {
    // Stagger list items
    const listItems = document.querySelectorAll(".security-checklist li");
    listItems.forEach((item, idx) => {
        gsap.fromTo(item,
            { opacity: 0, x: -30 },
            {
                opacity: 1,
                x: 0,
                duration: 1,
                ease: "power3.out",
                scrollTrigger: {
                    trigger: item,
                    start: "top 85%",
                    toggleActions: "play none none none"
                },
                delay: idx * 0.1
            }
        );
    });

    // Reveal SVG Shield container
    gsap.fromTo(".security-visual-side.reveal-scale-in",
        { opacity: 0, scale: 0.9, rotation: -8 },
        {
            opacity: 1,
            scale: 1,
            rotation: 0,
            duration: 1.8,
            ease: "power2.out",
            scrollTrigger: {
                trigger: ".security-visual-side",
                start: "top 80%",
                toggleActions: "play none none none"
            }
        }
    );
}

// 7. Financial Correctness Ledger Entry Sequences (Section 6)
function initLedgerAnimation() {
    // Fade up description text
    gsap.fromTo(".correctness-headline, .correctness-subtext",
        { opacity: 0, y: 30 },
        {
            opacity: 1,
            y: 0,
            duration: 1.2,
            stagger: 0.2,
            ease: "power3.out",
            scrollTrigger: {
                trigger: ".correctness-headline",
                start: "top 85%",
                toggleActions: "play none none none"
            }
        }
    );

    // Fade up ledger box
    gsap.fromTo(".ledger-visualizer.reveal-scale-in",
        { opacity: 0, scale: 0.97 },
        {
            opacity: 1,
            scale: 1,
            duration: 1.4,
            ease: "power2.out",
            scrollTrigger: {
                trigger: ".ledger-visualizer",
                start: "top 80%",
                toggleActions: "play none none none"
            }
        }
    );

    // Stagger the showing of transaction rows inside ledger
    const rows = document.querySelectorAll(".transaction-line");
    rows.forEach((row, idx) => {
        gsap.fromTo(row,
            { opacity: 0, x: -10 },
            {
                opacity: 1,
                x: 0,
                duration: 0.8,
                ease: "power2.out",
                scrollTrigger: {
                    trigger: ".ledger-table",
                    start: "top 75%",
                    toggleActions: "play none none none"
                },
                delay: 0.3 + (idx * 0.2)
            }
        );
    });
}

// 8. Resilience Timeline Node Scroll Reveal (Section 7)
function initTimelineReveal() {
    const timelineItems = document.querySelectorAll(".timeline-item");
    
    timelineItems.forEach((item) => {
        const marker = item.querySelector(".timeline-marker");
        const content = item.querySelector(".timeline-content");

        gsap.fromTo(marker,
            { scale: 0, opacity: 0 },
            {
                scale: 1,
                opacity: 1,
                duration: 0.5,
                scrollTrigger: {
                    trigger: item,
                    start: "top 85%",
                    toggleActions: "play none none none"
                }
            }
        );

        gsap.fromTo(content,
            { opacity: 0, y: 35 },
            {
                opacity: 1,
                y: 0,
                duration: 1,
                ease: "power2.out",
                scrollTrigger: {
                    trigger: item,
                    start: "top 80%",
                    toggleActions: "play none none none"
                }
            }
        );
    });
}

// 9. Operational Dashboard Reveal (Section 8)
function initDashboardAnimation() {
    gsap.fromTo(".dashboard-grid.reveal-scale-in",
        { opacity: 0, y: 30 },
        {
            opacity: 1,
            y: 0,
            duration: 1.2,
            ease: "power3.out",
            scrollTrigger: {
                trigger: ".dashboard-grid",
                start: "top 85%",
                toggleActions: "play none none none"
            }
        }
    );

    gsap.fromTo(".dashboard-footer-meta.reveal-fade-up",
        { opacity: 0, y: 40 },
        {
            opacity: 1,
            y: 0,
            duration: 1.4,
            ease: "power2.out",
            scrollTrigger: {
                trigger: ".dashboard-footer-meta",
                start: "top 80%",
                toggleActions: "play none none none"
            }
        }
    );
}

// 10. Engineering Achievements list fade-ups (Section 9)
function initAchievementsReveal() {
    const rows = document.querySelectorAll(".ach-row");

    rows.forEach((row, idx) => {
        gsap.fromTo(row,
            { opacity: 0, y: 20 },
            {
                opacity: 1,
                y: 0,
                duration: 0.8,
                ease: "power2.out",
                scrollTrigger: {
                    trigger: row,
                    start: "top 90%",
                    toggleActions: "play none none none"
                },
                delay: (idx % 3) * 0.05
            }
        );
    });
}
