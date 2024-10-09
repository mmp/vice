// pcf2go.cc
//
// hacked up version of pcf2bdf.cc https://github.com/ganaware/pcf2bdf to
// emit the information in go source code format. Only does enough
// for vice's needs with the STARS fonts.
//
// % /bin/rm stars-fonts.go ; for x in sddChar{,Outline}FontSet[B]*pcf; do echo $x; ./pcf2go $x -o stars-fonts.go; done; echo "}" >>| stars-fonts.go; gofmt -w stars-fonts.go && /bin/mv stars-fonts.go ~/vice

/*
 * see libXfont-1.4.5: src/bitmap/pcfread.c, pcfwrite.c, bcfread.c
 */

#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <string>

#if defined(_MSC_VER) // Microsoft Visual C++
#  include <io.h>
#  include <fcntl.h>
#  include <process.h>
#  define popen _popen

#elif defined(__CYGWIN__) // Cygnus GNU Win32 gcc
#  include <io.h>
#  include <sys/fcntl.h>

#else
#  define _setmode(fd, mode)

#endif


// miscellaneous definition ///////////////////////////////////////////////////


typedef bool bool8;
typedef unsigned char uint8;
typedef unsigned char byte8;
typedef short int16;
typedef unsigned short uint16;
typedef int int32;
typedef unsigned int uint32;

// section ID
enum type32 {
  PCF_PROPERTIES	= (1 << 0),
  PCF_ACCELERATORS	= (1 << 1),
  PCF_METRICS		= (1 << 2),
  PCF_BITMAPS		= (1 << 3),
  PCF_INK_METRICS	= (1 << 4),
  PCF_BDF_ENCODINGS	= (1 << 5),
  PCF_SWIDTHS		= (1 << 6),
  PCF_GLYPH_NAMES	= (1 << 7),
  PCF_BDF_ACCELERATORS	= (1 << 8),
};

// section format
struct format32 {
  uint32 id    :24;	// one of four constants below
  uint32 dummy :2;	// = 0 padding
  uint32 scan  :2;	// read bitmap by (1 << scan) bytes
  uint32 bit   :1;	// 0:LSBit first, 1:MSBit first
  uint32 byte  :1;	// 0:LSByte first, 1:MSByte first
  uint32 glyph :2;	// a scanline of gryph is aligned by (1 << glyph) bytes
  bool is_little_endian(void) { return !byte; }
};
// format32.id is:
#define PCF_DEFAULT_FORMAT     0
#define PCF_INKBOUNDS          2
#define PCF_ACCEL_W_INKBOUNDS  1
#define PCF_COMPRESSED_METRICS 1
// BDF file is outputed: MSBit first and MSByte first
const format32 BDF_format = { PCF_DEFAULT_FORMAT, 0, 0, 1, 1, 0 };

// string or value
union sv {
  char *s;
  int32 v;
};

// metric informations
struct metric_t
{
  int16	leftSideBearing;  // leftmost coordinate of the gryph
  int16	rightSideBearing; // rightmost coordinate of the gryph
  int16	characterWidth;   // offset to next gryph
  int16	ascent;           // pixels below baseline
  int16	descent;          // pixels above Baseline
  uint16	attributes;

  byte8 *bitmaps;         // bitmap pattern of gryph
  int32 swidth;           // swidth
  sv glyphName;           // name of gryph

  metric_t(void)
  {
    bitmaps = NULL;
    glyphName.s = NULL;
  }

  // gryph width
  int16 widthBits(void) { return rightSideBearing - leftSideBearing; }
  // gryph height
  int16 height(void) { return ascent + descent; }
  // byts for one scanline
  int16 widthBytes(format32 f)
  {
    return bytesPerRow(widthBits(), 1 << f.glyph);
  }
  static int16 bytesPerRow(int bits, int nbytes)
  {
    return nbytes == 1 ?  ((bits +  7) >> 3)        // pad to 1 byte
      :    nbytes == 2 ? (((bits + 15) >> 3) & ~1)  // pad to 2 bytes
      :    nbytes == 4 ? (((bits + 31) >> 3) & ~3)  // pad to 4 bytes
      :    nbytes == 8 ? (((bits + 63) >> 3) & ~7)  // pad to 8 bytes
      :    0;
  }
};

#define GLYPHPADOPTIONS 4

#define make_charcode(row,col) (row * 256 + col)
#define NO_SUCH_CHAR 0xffff


// global variables ///////////////////////////////////////////////////////////


// table of contents
int32 nTables;
struct table_t {
  type32 type;		// section ID
  format32 format;	// section format
  int32 size;		// size of section
  int32 offset;		// byte offset from the beginning of the file
} *tables;

// properties section
int32 nProps;		// number of properties
struct props_t {	// property
  sv name;		// name of property
  bool8 isStringProp;	// whether this property is a string (or a value)
  sv value;		// the value of this property
} *props;
int32 stringSize;	// size of string
char *string;		// string used in property

// accelerators section
struct accelerators_t {
  bool8	   noOverlap;		/* true if:
				 * max(rightSideBearing - characterWidth) <=
				 * minbounds->metrics.leftSideBearing */
  bool8	   constantMetrics;
  bool8	   terminalFont;	/* true if:
				 * constantMetrics && leftSideBearing == 0 &&
				 * rightSideBearing == characterWidth &&
				 * ascent == fontAscent &&
				 * descent == fontDescent */
  bool8	   constantWidth;	/* true if:
				 * minbounds->metrics.characterWidth
				 * ==
				 * maxbounds->metrics.characterWidth */
  bool8	   inkInside;		/* true if for all defined glyphs:
				 * 0 <= leftSideBearing &&
				 * rightSideBearing <= characterWidth &&
				 * -fontDescent <= ascent <= fontAscent &&
				 * -fontAscent <= descent <= fontDescent */
  bool8	   inkMetrics;		/* ink metrics != bitmap metrics */
  bool8    drawDirection;       /* 0:L->R 1:R->L*/
  int32	   fontAscent;
  int32	   fontDescent;
  int32	   maxOverlap;
  metric_t minBounds;
  metric_t maxBounds;
  metric_t ink_minBounds;
  metric_t ink_maxBounds;
} accelerators;

// metrics section
int32 nMetrics;
metric_t *metrics;

// bitmaps section
int32 nBitmaps;
uint32 *bitmapOffsets;
uint32 bitmapSizes[GLYPHPADOPTIONS];
byte8 *bitmaps;		// bitmap patterns of the gryph
int32 bitmapSize;	// size of bitmaps

// encodings section
uint16 firstCol;
uint16 lastCol;
uint16 firstRow;
uint16 lastRow;
uint16 defaultCh;	// default character
uint16 *encodings;
int nEncodings;		// number of encodings
int nValidEncodings;	// number of valid encodings

// swidths section
int32 nSwidths;

// glyph names section
int32 nGlyphNames;
int32 glyphNamesSize;
char *glyphNames;


// other globals
FILE *ifp;		// input file pointer
FILE *ofp;		// output file pointer
long read_bytes;	// read bytes
format32 format;	// current section format
metric_t fontbbx;	// font bounding box
bool verbose;		// show messages verbosely


// miscellaneous functions ////////////////////////////////////////////////////


int error_exit(const char *str)
{
  fprintf(stderr, "pcf2bdf: %s\n", str);
  exit(1);
  return 1;
}
int error_invalid_exit(const char *str)
{
  fprintf(stderr, "pcf2bdf: <%s> invalid PCF file\n", str);
  exit(1);
  return 1;
}

void check_int32_min(const char *indent, const char *str, int32 value, int32 min)
{
  if (!(min <= value))
  {
    fprintf(stderr, "pcf2bdf: <%s>=%d is out of range (must be >= %d)\n",
            str, value, min);
    exit(1);
  }
  else
  {
    if (verbose)
    {
      fprintf(stderr, "%s%s = %d\n", indent, str, value);
    }
  }
}

int check_memory(void *ptr)
{
  if (!ptr)
  {
    return error_exit("out of memory");
  }
  return 0;
}


byte8 *read_byte8s(byte8 *mem, size_t size)
{
  size_t read_size =  fread(mem, 1, size, ifp);
  if (read_size != size)
  {
    error_exit("unexpected eof");
  }
  read_bytes += size;
  return mem;
}


char read8(void)
{
  int a = fgetc(ifp);
  read_bytes ++;
  if (a == EOF)
  {
    return (char)error_exit("unexpected eof");
  }
  return (char)a;
}
bool8 read_bool8(void)
{
  return (bool8)!!read8();
}
uint8 read_uint8(void)
{
  return (uint8)read8();
}


/* These all return int rather than int16 in order to handle values
 * between 32768 and 65535 more gracefully.
 */
int make_int16(int a, int b)
{
  int value;
  value  = (a & 0xff) << 8;
  value |= (b & 0xff);
  return value;
}
int read_int16_big(void)
{
  int a = read8();
  int b = read8();
  return make_int16(a, b);
}
int read_int16_little(void)
{
  int a = read8();
  int b = read8();
  return make_int16(b, a);
}
int read_int16(void)
{
  if (format.is_little_endian())
  {
    return read_int16_little();
  }
  else
  {
    return read_int16_big();
  }
}


int32 make_int32(int a, int b, int c, int d)
{
  int32 value;
  value  = (int32)(a & 0xff) << 24;
  value |= (int32)(b & 0xff) << 16;
  value |= (int32)(c & 0xff) <<  8;
  value |= (int32)(d & 0xff);
  return value;
}
int32 read_int32_big(void)
{
  int a = read8();
  int b = read8();
  int c = read8();
  int d = read8();
  return make_int32(a, b, c, d);
}
int32 read_int32_little(void)
{
  int a = read8();
  int b = read8();
  int c = read8();
  int d = read8();
  return make_int32(d, c, b, a);
}
int32 read_int32(void)
{
  if (format.is_little_endian())
  {
    return read_int32_little();
  }
  else
  {
    return read_int32_big();
  }
}
uint32 read_uint32(void)
{
  return (uint32)read_int32();
}
format32 read_format32_little(void)
{
  int32 v = read_int32_little();
  format32 f;
  f.id     = v >> 8;
  f.dummy  = 0;
  f.scan   = v >> 4;
  f.bit    = v >> 3;
  f.byte   = v >> 2;
  f.glyph  = v >> 0;
  return f;
}


void skip(int n)
{
  for (; 0 < n; n--)
  {
    read8();
  }
}


void bit_order_invert(byte8 *data, int size)
{
  static const byte8 invert[16] =
  { 0, 8, 4, 12, 2, 10, 6, 14, 1, 9, 5, 13, 3, 11, 7, 15 };
  for (int i = 0; i < size; i++)
  {
    data[i] = (invert[data[i] & 15] << 4) | invert[(data[i] >> 4) & 15];
  }
}
void two_byte_swap(byte8 *data, int size)
{
  size &= ~1;
  for (int i = 0; i < size; i += 2)
  {
    byte8 tmp = data[i];
    data[i] = data[i + 1];
    data[i + 1] = tmp;
  }
}
void four_byte_swap(byte8 *data, int size)
{
  size &= ~3;
  for (int i = 0; i < size; i += 4)
  {
    byte8 tmp = data[i];
    data[i] = data[i + 3];
    data[i + 3] = tmp;
    tmp = data[i + 1];
    data[i + 1] = data[i + 2];
    data[i + 2] = tmp;
  }
}


// main ///////////////////////////////////////////////////////////////////////


// search and seek a section of 'type'
bool seek(type32 type)
{
  for (int i = 0; i < nTables; i++)
  {
    if (tables[i].type == type)
    {
      int s = tables[i].offset - read_bytes;
      if (s < 0)
      {
	error_invalid_exit("seek");
      }
      skip(s);
      return true;
    }
  }
  return false;
}


// does a section of 'type' exist?
bool is_exist_section(type32 type)
{
  for (int i = 0; i < nTables; i++)
  {
    if (tables[i].type == type)
    {
      return true;
    }
  }
  return false;
}


// read metric information
void read_metric(metric_t *m)
{
  m->leftSideBearing  = read_int16();
  m->rightSideBearing = read_int16();
  m->characterWidth   = read_int16();
  m->ascent           = read_int16();
  m->descent          = read_int16();
  m->attributes       = read_int16();
}


// read compressed metric information
void read_compressed_metric(metric_t *m)
{
  m->leftSideBearing  = (int16)read_uint8() - 0x80;
  m->rightSideBearing = (int16)read_uint8() - 0x80;
  m->characterWidth   = (int16)read_uint8() - 0x80;
  m->ascent           = (int16)read_uint8() - 0x80;
  m->descent          = (int16)read_uint8() - 0x80;
  m->attributes       = 0;
}


void verbose_metric(metric_t *m, const char *name)
{
  if (verbose)
  {
    fprintf(stderr, "\t%s.leftSideBearing  = %d\n", name, m->leftSideBearing);
    fprintf(stderr, "\t%s.rightSideBearing = %d\n", name, m->rightSideBearing);
    fprintf(stderr, "\t%s.characterWidth   = %d\n", name, m->characterWidth);
    fprintf(stderr, "\t%s.ascent           = %d\n", name, m->ascent);
    fprintf(stderr, "\t%s.descent          = %d\n", name, m->descent);
    fprintf(stderr, "\t%s.attributes       = %04x\n", name, m->attributes);
  }
}


// read accelerators section
void read_accelerators(void)
{
  format = read_format32_little();
  if (!(format.id == PCF_DEFAULT_FORMAT ||
	format.id == PCF_ACCEL_W_INKBOUNDS))
  {
    error_invalid_exit("accelerators");
  }
  
  accelerators.noOverlap       = read_bool8();
  accelerators.constantMetrics = read_bool8();
  accelerators.terminalFont    = read_bool8();
  accelerators.constantWidth   = read_bool8();
  accelerators.inkInside       = read_bool8();
  accelerators.inkMetrics      = read_bool8();
  accelerators.drawDirection   = read_bool8();
  /* dummy */ read_bool8();
  accelerators.fontAscent      = read_int32();
  accelerators.fontDescent     = read_int32();
  accelerators.maxOverlap      = read_int32();
  if (verbose)
  {
    fprintf(stderr, "\tnoOverlap       = %d\n", (int)accelerators.noOverlap);
    fprintf(stderr, "\tconstantMetrics = %d\n",
	    (int)accelerators.constantMetrics);
    fprintf(stderr, "\tterminalFont    = %d\n",
	    (int)accelerators.terminalFont);
    fprintf(stderr, "\tconstantWidth   = %d\n",
	    (int)accelerators.constantWidth);
    fprintf(stderr, "\tinkInside       = %d\n", (int)accelerators.inkInside);
    fprintf(stderr, "\tinkMetrics      = %d\n", (int)accelerators.inkMetrics);
    fprintf(stderr, "\tdrawDirection   = %d\n",
	    (int)accelerators.drawDirection);
    fprintf(stderr, "\tfontAscent      = %d\n", (int)accelerators.fontAscent);
    fprintf(stderr, "\tfontDescent     = %d\n", (int)accelerators.fontDescent);
    fprintf(stderr, "\tmaxOverlap      = %d\n", (int)accelerators.maxOverlap);
  }
  read_metric(&accelerators.minBounds);
  read_metric(&accelerators.maxBounds);
  verbose_metric(&accelerators.minBounds, "minBounds");
  verbose_metric(&accelerators.maxBounds, "maxBounds");
  if (format.id == PCF_ACCEL_W_INKBOUNDS)
  {
    read_metric(&accelerators.ink_minBounds);
    read_metric(&accelerators.ink_maxBounds);
    verbose_metric(&accelerators.ink_minBounds, "ink_minBounds");
    verbose_metric(&accelerators.ink_maxBounds, "ink_maxBounds");
  }
  else
  {
    accelerators.ink_minBounds = accelerators.minBounds;
    accelerators.ink_maxBounds = accelerators.maxBounds;
  }
}


// search a property named 'name', and return its string if it is a string
char *get_property_string(const char *name)
{
  for (int i = 0; i < nProps; i++)
  {
    if (strcmp(name, props[i].name.s) == 0)
    {
      if (props[i].isStringProp)
      {
	return props[i].value.s;
      }
      else
      {
	error_invalid_exit("property_string");
      }
    }
  }
  return NULL;
}


// search a property named 'name', and return its value if it is a value
int32 get_property_value(const char *name)
{
  for (int i = 0; i < nProps; i++)
  {
    if (strcmp(name, props[i].name.s) == 0)
    {
      if (props[i].isStringProp)
      {
	error_invalid_exit("property_value");
      }
      else
      {
	return props[i].value.v;
      }
    }
  }
  return -1;
}


// does a property named 'name' exist?
bool is_exist_property_value(const char *name)
{
  for (int i = 0; i < nProps; i++)
  {
    if (strcmp(name, props[i].name.s) == 0)
    {
      if (props[i].isStringProp)
      {
	return false;
      }
      else
      {
	return true;
      }
    }
  }
  return false;
}


int usage_exit(void)
{
  printf("usage: pcf2bdf [-v] [-o bdf file] [pcf file]\n");
  return 1;
}


std::string escape_quote(const char *p)
{
  std::string result;
  for (; *p; ++ p)
  {
    if (*p == '\'')
    {
      result.append("\\");
    }
    result.append(1, *p);
  }
  return result;
}


int main(int argc, char *argv[])
{
  int i;
  char *ifilename = NULL;
  char *ofilename = NULL;

  // read options
  for (i = 1; i < argc; i++)
  {
    if (argv[i][0] == '-')
    {
      if (argv[i][1] == 'v')
      {
	verbose = true;
      }
      else if (i + 1 == argc || argv[i][1] != 'o' || ofilename)
      {
	return usage_exit();
      }
      else
      {
	ofilename = argv[++i];
      }
    }
    else
    {
      if (ifilename)
      {
	return usage_exit();
      }
      else
      {
	ifilename = argv[i];
      }
    }
  }
  if (ifilename)
  {
    ifp = fopen(ifilename, "rb");
    if (!ifp)
    {
      return error_exit("failed to open input pcf file");
    }
  }
  else
  {
    _setmode(fileno(stdin), O_BINARY);
    ifp = stdin;
  }
  int32 version = read_int32_big();
  if ((version >> 16) == 0x1f9d || // compress'ed
      (version >> 16) == 0x1f8b)    // gzip'ed
  {
    if (!ifilename)
    {
      return error_exit("stdin is gzip'ed or compress'ed\n");
    }
    fclose(ifp);
    std::string cmd = "gzip -dc '";
    cmd.append(escape_quote(ifilename));
    cmd.append("'");
    ifp = popen(cmd.c_str(), "r");
    _setmode(fileno(ifp), O_BINARY);
    read_bytes = 0;
    if (!ifp)
    {
      return error_exit("failed to execute gzip\n");
    }
  }

  bool outfile_exists = false;
  if (ofilename)
  {
    FILE *f = fopen(ofilename, "r");
    if (f != nullptr) {
        outfile_exists = true;
        fclose(f);
        ofp = fopen(ofilename, "ab");
    } else
        ofp = fopen(ofilename, "wb");

    if (!ofp)
    {
      return error_exit("failed to open output bdf file");
    }
  }
  else
  {
    ofp = stdout;
  }

  // read PCF file ////////////////////////////////////////////////////////////

  // read table of contents
  if (read_bytes == 0)
  {
    version = read_int32_big();
  }
  if (version != make_int32(1, 'f', 'c', 'p'))
  {
    error_exit("this is not PCF file format");
  }
  nTables = read_int32_little();
  check_int32_min("", "nTables", nTables, 1);
  check_memory((tables = new table_t[nTables]));
  for (i = 0; i < nTables; i++)
  {
    tables[i].type   = (type32)read_int32_little();
    tables[i].format = read_format32_little();
    tables[i].size   = read_int32_little();
    tables[i].offset = read_int32_little();
  }
  
  // read properties section
  if (!seek(PCF_PROPERTIES))
  {
    error_exit("PCF_PROPERTIES does not found");
  }
  else
  {
    if (verbose)
    {
      fprintf(stderr, "PCF_PROPERTIES\n");
    }
  }
  format = read_format32_little();
  if (!(format.id == PCF_DEFAULT_FORMAT))
  {
    error_invalid_exit("properties(format)");
  }
  nProps = read_int32();
  check_int32_min("\t", "nProps", nProps, 1);
  check_memory((props = new props_t[nProps]));
  for (i = 0; i < nProps; i++)
  {
    props[i].name.v       = read_int32();
    props[i].isStringProp = read_bool8();
    props[i].value.v      = read_int32();
  }
  skip(3 - (((4 + 1 + 4) * nProps + 3) % 4));
  stringSize = read_int32();
  check_int32_min("\t", "stringSize", stringSize, 0);
  check_memory((string = new char[stringSize + 1]));
  read_byte8s((byte8 *)string, stringSize);
  string[stringSize] = '\0';
  for (i = 0; i < nProps; i++)
  {
    if (stringSize <= props[i].name.v)
    {
      error_invalid_exit("properties(name)");
    }
    props[i].name.s = string + props[i].name.v;
    if (verbose)
    {
      fprintf(stderr, "\t%s ", props[i].name.s);
    }
    if (props[i].isStringProp)
    {
      if (stringSize <= props[i].value.v)
      {
	error_invalid_exit("properties(value)");
      }
      props[i].value.s = string + props[i].value.v;
      if (verbose)
      {
	fprintf(stderr, "\"%s\"\n", props[i].value.s);
      }
    }
    else
    {
      if (verbose)
      {
	fprintf(stderr, "%d\n", props[i].value.v);
      }
    }
  }
  
  // read old accelerators section
  if (!is_exist_section(PCF_BDF_ACCELERATORS))
  {
    if (!seek(PCF_ACCELERATORS))
    {
      error_exit("PCF_ACCELERATORS and PCF_BDF_ACCELERATORS do not found");
    }
    else
    {
      if (verbose)
      {
	fprintf(stderr, "PCF_ACCELERATORS\n");
      }
      read_accelerators();
    }
  }
  else
  {
    if (verbose)
    {
      fprintf(stderr, "(PCF_ACCELERATORS)\n");
    }
  }
  
  // read metrics section
  if (!seek(PCF_METRICS))
  {
    error_exit("PCF_METRICS does not found");
  }
  else
  {
    if (verbose)
    {
      fprintf(stderr, "PCF_METRICS\n");
    }
  }
  format = read_format32_little();
  switch (format.id)
  {
    default:
      error_invalid_exit("metrics");
    case PCF_DEFAULT_FORMAT:
      nMetrics = read_int32();
      check_int32_min("\t", "nMetrics", nMetrics, 1);
      check_memory((metrics = new metric_t[nMetrics]));
      for (i = 0; i < nMetrics; i++)
      {
	read_metric(&metrics[i]);
      }
      break;
    case PCF_COMPRESSED_METRICS:
      if (verbose)
      {
	fprintf(stderr, "\tPCF_COMPRESSED_METRICS\n");
      }
      nMetrics = read_int16();
      check_int32_min("\t", "nMetrics", nMetrics, 1);
      check_memory((metrics = new metric_t[nMetrics]));
      for (i = 0; i < nMetrics; i++)
      {
	read_compressed_metric(&metrics[i]);
      }
      break;
  }
  fontbbx = metrics[0];
  for (i = 1; i < nMetrics; i++)
  {
    if (metrics[i].leftSideBearing < fontbbx.leftSideBearing)
    {
      fontbbx.leftSideBearing = metrics[i].leftSideBearing;
    }
    if (fontbbx.rightSideBearing < metrics[i].rightSideBearing)
    {
      fontbbx.rightSideBearing = metrics[i].rightSideBearing;
    }
    if (fontbbx.ascent < metrics[i].ascent)
    {
      fontbbx.ascent = metrics[i].ascent;
    }
    if (fontbbx.descent < metrics[i].descent)
    {
      fontbbx.descent = metrics[i].descent;
    }
  }
  
  // read bitmaps section
  if (!seek(PCF_BITMAPS))
  {
    error_exit("PCF_BITMAPS does not found");
  }
  else
  {
    if (verbose)
    {
      fprintf(stderr, "PCF_BITMAPS\n");
    }
  }
  format = read_format32_little();
  if (!(format.id == PCF_DEFAULT_FORMAT))
  {
    error_invalid_exit("bitmaps");
  }
  nBitmaps = read_int32();
  check_int32_min("\t", "nBitmaps", nBitmaps, nMetrics);
  check_memory((bitmapOffsets = new uint32[nBitmaps]));
  for (i = 0; i < nBitmaps; i++)
  {
    bitmapOffsets[i] = read_uint32();
  }
  for (i = 0; i < GLYPHPADOPTIONS; i++)
  {
    bitmapSizes[i] = read_uint32();
  }
  bitmapSize = bitmapSizes[format.glyph];
  check_int32_min("\t", "bitmapSize", bitmapSize, 0);
  check_memory((bitmaps = new byte8[bitmapSize]));
  read_byte8s(bitmaps, bitmapSize);
  //
  if (verbose)
  {
    fprintf(stderr, "\t1<<format.scan = %d\n", 1 << format.scan);
    fprintf(stderr, "\t%sSBit first\n", format.bit ? "M" : "L");
    fprintf(stderr, "\t%sSByte first\n", format.byte ? "M" : "L");
    fprintf(stderr, "\t1<<format.glyph = %d\n", 1 << format.glyph);
  }
  if (format.bit != BDF_format.bit)
  {
    if (verbose)
    {
      fprintf(stderr, "\tbit_order_invert()\n");
    }
    bit_order_invert(bitmaps, bitmapSize);
  }
  if ((format.bit == format.byte) !=  (BDF_format.bit == BDF_format.byte))
  {
    switch (1 << (BDF_format.bit == BDF_format.byte ?
		  format.scan : BDF_format.scan))
    {
      case 1: break;
      case 2:
	if (verbose)
	{
	  fprintf(stderr, "\ttwo_byte_swap()\n");
	}
	two_byte_swap(bitmaps, bitmapSize);
	break;
      case 4:
	if (verbose)
	{
	  fprintf(stderr, "\tfour_byte_swap()\n");
	}
	four_byte_swap(bitmaps, bitmapSize);
	break;
    }
  }
  //
  for (i = 0; i < nMetrics; i++)
  {
    metric_t &m = metrics[i];
    m.bitmaps = bitmaps + bitmapOffsets[i];
  }
  
  // ink metrics section is ignored
  
  // read encodings section
  if (!seek(PCF_BDF_ENCODINGS))
  {
    error_exit("PCF_BDF_ENCODINGS does not found");
  }
  else
  {
    if (verbose)
    {
      fprintf(stderr, "PCF_ENCODINGS\n");
    }
  }
  format = read_format32_little();
  if (!(format.id == PCF_DEFAULT_FORMAT))
  {
    error_invalid_exit("encoding");
  }
  firstCol  = read_int16();
  lastCol   = read_int16();
  firstRow  = read_int16();
  lastRow   = read_int16();
  defaultCh = read_int16();
  if (verbose)
  {
    fprintf(stderr, "\tfirstCol  = %X\n", firstCol);
    fprintf(stderr, "\tlastCol   = %X\n", lastCol);
    fprintf(stderr, "\tfirstRow  = %X\n", firstRow);
    fprintf(stderr, "\tlastRow   = %X\n", lastRow);
    fprintf(stderr, "\tdefaultCh = %X\n", defaultCh);
  }
  if (!(firstCol <= lastCol))
  {
    error_invalid_exit("firstCol, lastCol");
  }
  if (!(firstRow <= lastRow))
  {
    error_invalid_exit("firstRow, lastRow");
  }
  nEncodings = (lastCol - firstCol + 1) * (lastRow - firstRow + 1);
  check_memory((encodings = new uint16[nEncodings]));
  for (i = 0; i < nEncodings; i++)
  {
    encodings[i] = read_int16();
    if (encodings[i] != NO_SUCH_CHAR)
    {
      nValidEncodings ++;
    }
  }

  // read swidths section
  if (seek(PCF_SWIDTHS))
  {
    if (verbose)
    {
      fprintf(stderr, "PCF_SWIDTHS\n");
    }
    format = read_format32_little();
    if (!(format.id == PCF_DEFAULT_FORMAT))
    {
      error_invalid_exit("encoding");
    }
    nSwidths = read_int32();
    if (nSwidths != nMetrics)
    {
      error_exit("nSwidths != nMetrics");
    }
    for (i = 0; i < nSwidths; i++)
    {
      metrics[i].swidth = read_int32();
    }
  }
  else
  {
    if (verbose)
    {
      fprintf(stderr, "no PCF_SWIDTHS\n");
    }
    int32 rx = get_property_value("RESOLUTION_X");
    if (rx <= 0)
    {
      rx = (int)(get_property_value("RESOLUTION") / 100.0 * 72.27) ;
    }
    double p = get_property_value("POINT_SIZE") / 10.0;
    nSwidths = nMetrics;
    for (i = 0; i < nSwidths; i++)
    {
      metrics[i].swidth =
	(int)(metrics[i].characterWidth / (rx / 72.27) / (p / 1000));
    }
  }
  
  // read glyph names section
  if (seek(PCF_GLYPH_NAMES))
  {
    if (verbose)
    {
      fprintf(stderr, "PCF_GLYPH_NAMES\n");
    }
    format = read_format32_little();
    if (!(format.id == PCF_DEFAULT_FORMAT))
    {
      error_invalid_exit("encoding");
    }
    nGlyphNames = read_int32();
    if (nGlyphNames != nMetrics)
    {
      error_exit("nGlyphNames != nMetrics");
    }
    for (i = 0; i < nGlyphNames; i++)
    {
      metrics[i].glyphName.v = read_int32();
    }
    glyphNamesSize = read_int32();
    check_int32_min("\t", "glyphNamesSize", glyphNamesSize, 0);
    check_memory((glyphNames = new char[glyphNamesSize + 1]));
    read_byte8s((byte8 *)glyphNames, glyphNamesSize);
    glyphNames[glyphNamesSize] = '\0';
    for (i = 0; i < nGlyphNames; i++)
    {
      if (glyphNamesSize <= metrics[i].glyphName.v)
      {
	error_invalid_exit("glyphNames");
      }
      metrics[i].glyphName.s = glyphNames + metrics[i].glyphName.v;
    }
  }
  else
  {
    if (verbose)
    {
      fprintf(stderr, "no PCF_GLYPH_NAMES\n");
    }
  }
  
  // read BDF style accelerators section
  if (seek(PCF_BDF_ACCELERATORS))
  {
    if (verbose)
    {
      fprintf(stderr, "PCF_BDF_ACCELERATORS\n");
    }
    read_accelerators();
  }
  else
  {
    if (verbose)
    {
      fprintf(stderr, "no PCF_BDF_ACCELERATORS\n");
    }
  }
  
  // write go file ///////////////////////////////////////////////////////////
  if (!outfile_exists) {
      fprintf(ofp, R"(
// Automatically generated from STARS PCF font files using util/pcg2go.cc

package main

type STARSFont struct {
    PointSize int
    Width, Height int
    Glyphs []STARSGlyph
}

type STARSGlyph struct {
    Name string
    StepX int
    Bounds [2]int
    Offset [2]int
    Bitmap []uint32
}

var starsFonts map[string]STARSFont = map[string]STARSFont{
)");
   }

  char *fontname = strdup(ifilename);
  char *period = strrchr(ifilename, '.');
  if (period != NULL) *period = '\0';

  fprintf(ofp, "\"%s\": STARSFont{\n", fontname);

  fprintf(ofp, "    PointSize: %d,\n", get_property_value("POINT_SIZE") / 10);
  fprintf(ofp, "    Width: %d,\n    Height:%d,\n", fontbbx.widthBits(), fontbbx.height());
  fprintf(ofp, "    Glyphs: []STARSGlyph{\n");

  for (i = 0; i < nEncodings; i++)
  {
    if (encodings[i] == NO_SUCH_CHAR)
    {
      continue;
    }

    int col = i % (lastCol - firstCol + 1) + firstCol;
    int row = i / (lastCol - firstCol + 1) + firstRow;
    uint16 charcode = make_charcode(row, col);
    if (!(encodings[i] < nMetrics))
    {
      error_invalid_exit("encodings");
    }
    metric_t &m = metrics[encodings[i]];
    fprintf(ofp, "%d: STARSGlyph{", charcode);
    if (m.glyphName.s)
        fprintf(ofp, " Name: \"%s\", ", m.glyphName.s);
    fprintf(ofp, "StepX: %d, ", m.characterWidth);
    fprintf(ofp, "Bounds: [2]int{%d, %d}, ", m.widthBits(), m.height());
    fprintf(ofp, "Offset: [2]int{%d, %d}, ", m.leftSideBearing, -m.descent);
    fprintf(ofp, "Bitmap: []uint32{");

    int widthBytes = m.widthBytes(format);
    int w = (m.widthBits() + 7) / 8;
    w = w < 1 ? 1 : w;
    byte8 *b = m.bitmaps;
    for (int r = 0; r < m.height(); r++)
    {
      fprintf(ofp, "0x");
      for (int c = 0; c < widthBytes; c++)
      {
        if (c < w)
        {
          fprintf(ofp, "%02X", *b);
        }
        else
          fprintf(ofp, "00");
        b++;
      }
      if (r+1 < m.height())
          fprintf(ofp, ", ");
    }
    fprintf(ofp, "}},\n");
  }

  fprintf(ofp, "},\n},\n");
  return 0;
}
