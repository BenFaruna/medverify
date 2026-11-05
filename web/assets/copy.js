// User-facing copy for the web client. This is the only place page text lives;
// index.html and app.js read keys from here.
//
// Pidgin was approved by the project owner on 2026-11-05. Keep `pcm` in
// REVIEWED_LANGUAGES and ASSISTANT_LANGUAGES=en,pcm on the server so the page
// and the backend fixed text stay in the same language. See
// docs/pidgin-copy-review.md.

export const COPY = {
  en: {
    pageTitle: "MedVerify NG",
    metaDescription:
      "Compare a medicine pack photo with NAFDAC records and read an explainable result.",
    skipToConversation: "Skip to conversation",
    conversation: "Conversation",
    tagline: "Compare a medicine pack with NAFDAC records.",
    settings: "Settings",
    resultLanguage: "Result language",
    languageHintReady: "English and Pidgin are available.",
    apiAddress: "API address",

    notice:
      "This tool compares the pack photo with NAFDAC records. It is not medical advice. Ask a pharmacist or doctor about your medicine.",

    startHere: "Start here",
    introLede:
      "Add photos of a medicine pack and I will compare what is printed on it with NAFDAC records.",
    tipFront: "Front, with the product name",
    tipBack: "Back, with the NRN",
    tipSide: "A side, with the ingredients",
    introHint:
      "Up to four photos. Lay the pack flat, fill the frame, and avoid glare. Not found in NAFDAC records does not mean a medicine is fake: our data can be incomplete. An unreadable photo is not a judgement of the pack.",

    suggestedActions: "Suggested actions",
    photosReady: "Photos ready to send",
    addPhotosOfPack: "Add photos of the pack",
    addPhotos: "Add photos",
    yourQuestion: "Your question",
    addPhotosToBegin: "Add photos to begin",
    checkThisPack: "Check this pack",
    noscript: "This page needs JavaScript to upload photos.",

    photoAlt: "Photo {index} of the pack",
    photoThumbAlt: "Photo {index}",
    removePhoto: "Remove photo {index}",

    errorTitle: "That did not work",
    connectionFailed:
      "The connection failed. Check your network and try again; the same photos will not create a second scan.",
    errorUnauthorized: "That API key was not accepted.",
    errorInsufficientScope: "This API key cannot create scans.",
    errorRateLimited: "Too many requests. Wait a moment and try again.",
    errorQuotaExceeded: "This API key has used its daily allowance.",
    errorTooManyImages: "Send at most four photos.",
    errorImageTooLarge: "One photo is too large even after resizing.",
    errorPayloadTooLarge: "The upload is too large. Try fewer photos.",
    errorUnsupportedFormat: "Use JPEG, PNG, or WebP photos.",
    errorCorruptImage: "One photo could not be read. Try taking it again.",
    errorInvalidLanguage: "That language is not supported.",
    errorUnavailable: "The service is temporarily unavailable. Try again shortly.",
    errorGeneric: "Something went wrong ({code}). Try again.",
    noApiKey: "No API key is set for this page.",
    takingTooLong: "This is taking longer than usual. Reload the page to check again.",

    preparingPhotos: "Preparing your photos…",
    uploading: "Uploading…",
    stillChecking: "Still checking. This can take a few seconds…",
    checkingRecords: "Checking the records…",

    noVerdict: "This pack has no verdict yet.",
    verdictNeedsLook: "This pack needs a closer look.",
    resultKicker: "Result",
    confidence: "Confidence: {value}",
    flagsHeading: "Flags",
    flagSeverity: "A {severity} severity finding: {type}.",
    sourceDocument: "Source document",
    whyHeading: "Why",
    readFromPack: "Read from the pack",
    nafdacRecords: "NAFDAC records",
    recordNotFound:
      "No record matched the read details. Our dataset can be incomplete, so this is not proof of a problem.",
    recordCandidateOnly: "A possible match was found but it was not confirmed.",
    recordAmbiguous: "More than one possible match was found.",
    recordUnreadableInput: "The pack details could not be read well enough to compare.",
    recordInconclusive: "The record comparison was inconclusive.",
    comparedAgainst: "Compared against {count} NAFDAC records.",
    sources: "Sources:",

    verdictConsistent:
      "The details printed on this pack agree with the NAFDAC record found for it.",
    verdictMismatch:
      "A detail printed on this pack disagrees with the NAFDAC record found for it.",
    verdictNotFound:
      "No matching record was found in our NAFDAC data. Our data can be incomplete, so this does not mean the pack is fake.",
    verdictFlagged: "A NAFDAC alert or a safety check applies to this pack.",
    verdictNeedsReview:
      "The evidence is ambiguous or incomplete, so this pack needs a closer look.",
    verdictUnreadable:
      "The photos could not be read clearly. Please take another photo.",

    fieldNrn: "NRN",
    fieldProductName: "Product name",
    fieldManufacturer: "Manufacturer",
    fieldBatchNo: "Batch number",
    fieldManufactured: "Manufactured",
    fieldExpiry: "Expiry",

    suggestionRecord: "What does the NAFDAC record say about this product?",
    suggestionIngredients: "What are the listed ingredients?",
    suggestionLeaflet: "Where can I read the leaflet?",
    askHelp:
      "Answers come only from NAFDAC records and leaflets, with citations. This is not medical advice.",

    checkAnotherPack: "Check another pack",
    readyNextPack: "Ready for the next pack. Add its photos below.",
    photosFit: "Only {max} photos fit in one check.",
    addUpTo: "Add up to {max} photos: front, back and a side.",
    photosReadyCount: "{count} of {max} photos ready.",
    askAboutPack: "Ask about this pack",
    addPhotosSelected: "Add photos of the pack, {count} of {max} selected",
    ask: "Ask",
  },

  pcm: {
    pageTitle: "MedVerify NG",
    metaDescription:
      "Compare medicine pack photo with NAFDAC records and read explainable result.",
    skipToConversation: "Go straight to conversation",
    conversation: "Conversation",
    tagline: "Compare medicine pack with NAFDAC records.",
    settings: "Settings",
    resultLanguage: "Language for the result",
    languageHintReady: "English and Pidgin dey available.",
    apiAddress: "API address",

    notice:
      "This tool dey compare the pack photo with NAFDAC records. E no be medical advice. Ask pharmacist or doctor about your medicine.",

    startHere: "Start here",
    introLede:
      "Add photos of medicine pack, I go compare wetin dey printed on am with NAFDAC records.",
    tipFront: "Front, wey get the product name",
    tipBack: "Back, wey get the NRN",
    tipSide: "One side, wey get the ingredients",
    introHint:
      "You fit add up to four photos. Put the pack flat, make am fill the frame, and avoid too much light wey go shine. If we no find am for NAFDAC records, e no mean say the medicine fake: our data fit no complete. If photo no clear, e no mean say the pack get problem.",

    suggestedActions: "Questions wey you fit ask",
    photosReady: "Photos wey ready to send",
    addPhotosOfPack: "Add photos of the pack",
    addPhotos: "Add photos",
    yourQuestion: "Your question",
    addPhotosToBegin: "Add photos to begin",
    checkThisPack: "Check this pack",
    noscript: "This page need JavaScript before you fit upload photos.",

    photoAlt: "Photo {index} of the pack",
    photoThumbAlt: "Photo {index}",
    removePhoto: "Remove photo {index}",

    errorTitle: "That one no work",
    connectionFailed:
      "Connection fail. Check your network and try again; the same photos no go create another scan.",
    errorUnauthorized: "We no accept that API key.",
    errorInsufficientScope: "This API key no fit create scan.",
    errorRateLimited: "Too many requests. Wait small, then try again.",
    errorQuotaExceeded: "This API key don use the allowance for today.",
    errorTooManyImages: "Send no more than four photos.",
    errorImageTooLarge: "One photo still too big even after we resize am.",
    errorPayloadTooLarge: "The upload too big. Try fewer photos.",
    errorUnsupportedFormat: "Use JPEG, PNG, or WebP photos.",
    errorCorruptImage: "We no fit read one photo. Try take am again.",
    errorInvalidLanguage: "We no support that language.",
    errorUnavailable: "The service no dey available for now. Try again small time.",
    errorGeneric: "Something go wrong ({code}). Try again.",
    noApiKey: "No API key dey for this page.",
    takingTooLong: "This one dey take longer than usual. Reload the page to check again.",

    preparingPhotos: "We dey prepare your photos…",
    uploading: "We dey upload…",
    stillChecking: "We still dey check. E fit take small seconds…",
    checkingRecords: "We dey check the records…",

    noVerdict: "This pack no get verdict yet.",
    verdictNeedsLook: "This pack need closer look.",
    resultKicker: "Result",
    confidence: "Confidence: {value}",
    flagsHeading: "Flags",
    flagSeverity: "One {severity} finding: {type}.",
    sourceDocument: "Source document",
    whyHeading: "Why",
    readFromPack: "Wetin we read from the pack",
    nafdacRecords: "NAFDAC records",
    recordNotFound:
      "No record match wetin we read. Our dataset fit no complete, so this no be proof say something wrong.",
    recordCandidateOnly: "We see one possible match but we no confirm am.",
    recordAmbiguous: "We see more than one possible match.",
    recordUnreadableInput:
      "We no fit read the pack details well enough to compare.",
    recordInconclusive: "The record comparison no conclude.",
    comparedAgainst: "We compare am with {count} NAFDAC records.",
    sources: "Sources:",

    verdictConsistent:
      "Wetin dey printed on this pack agree with the NAFDAC record we find for am.",
    verdictMismatch:
      "One detail wey printed on this pack no agree with the NAFDAC record we find for am.",
    verdictNotFound:
      "We no find matching record for our NAFDAC data. Our data fit no complete, so this no mean say the pack fake.",
    verdictFlagged: "One NAFDAC alert or safety check concern this pack.",
    verdictNeedsReview:
      "The evidence no clear or e no complete, so this pack need closer look.",
    verdictUnreadable:
      "We no fit read the photos well. Abeg take another photo.",

    fieldNrn: "NRN",
    fieldProductName: "Name of product",
    fieldManufacturer: "Manufacturer",
    fieldBatchNo: "Batch number",
    fieldManufactured: "Manufactured",
    fieldExpiry: "Expiry",

    suggestionRecord: "Wetin NAFDAC record talk about this product?",
    suggestionIngredients: "Wetin be the ingredients wey dey listed?",
    suggestionLeaflet: "Where I fit read the leaflet?",
    askHelp:
      "Answers dey come only from NAFDAC records and leaflets, with citations. This no be medical advice.",

    checkAnotherPack: "Check another pack",
    readyNextPack: "We ready for the next pack. Add im photos below.",
    photosFit: "Only {max} photos fit for one check.",
    addUpTo: "Add up to {max} photos: front, back and one side.",
    photosReadyCount: "{count} of {max} photos ready.",
    askAboutPack: "Ask about this pack",
    addPhotosSelected: "Add photos of the pack, {count} of {max} selected",
    ask: "Ask",
  },
};

// Pidgin was approved by the project owner on 2026-11-05.
export const REVIEWED_LANGUAGES = new Set(["en", "pcm"]);

export function copyFor(language) {
  const requested = String(language || "en").toLowerCase();
  const active = REVIEWED_LANGUAGES.has(requested) ? requested : "en";
  return { language: active, copy: COPY[active] || COPY.en };
}
