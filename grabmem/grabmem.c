#include <errno.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <unistd.h>

void errout(char *action, int err_code);
size_t parse_arg(char *arg);
void do_mmap(size_t mmap_kb);

int main(int argc, char **argv)
{
  if (argc < 3) {
    printf("usage: grabmem <malloc_kb> <mmap_kb>\n");
    exit(1);
  }
  size_t malloc_kb = parse_arg(argv[1]);
  size_t mmap_kb = parse_arg(argv[2]);

  size_t i;
  for (i = 0; i < malloc_kb; i++) {
    void *ptr = malloc(1024);
    if (ptr == NULL) {
      int err_code = errno;
      errout("malloc-ing", err_code);
    }
  }

  do_mmap(mmap_kb);

  while (1) {
    sleep(3600);
  }
}

void do_mmap(size_t mmap_kb)
{
  if (mmap_kb == 0) {
    return;
  }
  size_t mmap_b = mmap_kb * 1024;

  void *ptr = mmap(NULL, mmap_b, PROT_EXEC|PROT_READ|PROT_WRITE,
      MAP_ANONYMOUS|MAP_PRIVATE, -1, 0);
  if (ptr == MAP_FAILED) {
    int err_code = errno;
    errout("mmap-ing", err_code);
  }

  int page_size = getpagesize();
  int *mmapped = (int *) ptr;
  size_t i;
  for (i = 0; i < mmap_b / sizeof(int); i += page_size) {
    mmapped[i] = 42;
  }
}

size_t parse_arg(char *arg)
{
  size_t parsed;
  int matches = sscanf(arg, "%zu", &parsed);
  if (matches != 1) {
    int err_code = errno;
    errout("parsing arg", err_code);
  }
  return parsed;
}

void errout(char *action, int err_code)
{
  printf("error: %s: %s\n", action, strerror(err_code));
  exit(1);
}
