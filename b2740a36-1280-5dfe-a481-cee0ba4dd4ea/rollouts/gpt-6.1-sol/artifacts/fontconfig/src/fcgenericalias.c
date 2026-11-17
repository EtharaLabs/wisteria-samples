/* Copyright (C) 2026 fontconfig Authors */
/* SPDX-License-Identifier: HPND */

#include "fcint.h"

#include <stdint.h>

static unsigned int
fc_generic_family_hash (register const char *str, register FC_GPERF_SIZE_T len);

static const struct FcGenericFamilyEntry *
fc_generic_family_lookup (register const char *str, register FC_GPERF_SIZE_T len);

#define GPERF_DOWNCASE    1
#define GPERF_CASE_STRCMP 1
static int
gperf_case_strcmp (register const char *s1, register const char *s2)
{
    return FcStrCmpIgnoreBlanksAndCase ((const FcChar8 *)s1, (const FcChar8 *)s2);
}

#include "fcgenericfamily.h"

uint32_t
FcGenericAliasGetClassification (const char *family)
{
    const struct FcGenericFamilyEntry *entry;
    size_t                             len;
    uint32_t                           result = FC_FAMILY_UNKNOWN;

    if (!family)
	return FC_FAMILY_UNKNOWN;

    len = strlen (family);
    entry = fc_generic_family_lookup (family, len);
    if (entry)
	result = entry->classification;

    return result;
}

/* Complete older cached patterns using exactly the classification used by
 * the scanner. Callers supply a writable pattern; cache mappings stay intact. */
FcBool
FcPatternSetGenericFamily (FcPattern *pat)
{
    FcPatternElt  *elt;
    FcValueListPtr l;
    int            generic_family = FC_FAMILY_UNKNOWN;

    elt = FcPatternObjectFindElt (pat, FC_FAMILY_OBJECT);
    if (elt) {
	for (l = FcPatternEltValues (elt); l; l = FcValueListNext (l)) {
	    FcValue v = FcValueCanonicalize (&l->value);

	    if (v.type == FcTypeString) {
		uint32_t field = FcGenericAliasGetClassification ((const char *)v.u.s);

		if (field != 0) {
		    for (int b = 0; b < 15; b++) {
			if ((field & (1 << b)) != 0 &&
			    !FcPatternObjectAddInteger (pat, FC_GENERIC_FAMILY_OBJECT, b + 1))
			    return FcFalse;
		    }
		    return FcTrue;
		} else {
		    if (FcStrStrIgnoreCase (v.u.s, (FcChar8 *)"mono")) {
			generic_family = FC_FAMILY_MONO;
			break;
		    } else if (FcStrStrIgnoreCase (v.u.s, (FcChar8 *)"sans")) {
			generic_family = FC_FAMILY_SANS;
			break;
		    } else if (FcStrStrIgnoreCase (v.u.s, (FcChar8 *)"serif")) {
			generic_family = FC_FAMILY_SERIF;
			break;
		    } else if (FcStrStrIgnoreCase (v.u.s, (FcChar8 *)"emoji")) {
			generic_family = FC_FAMILY_EMOJI;
			break;
		    } else if (FcStrStrIgnoreCase (v.u.s, (FcChar8 *)"math")) {
			generic_family = FC_FAMILY_MATH;
			break;
		    }
		}
	    }
	}
    }
    return FcPatternObjectAddInteger (pat, FC_GENERIC_FAMILY_OBJECT, generic_family);
}
